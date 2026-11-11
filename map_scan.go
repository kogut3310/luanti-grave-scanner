package main

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	_ "modernc.org/sqlite"
)

type mapReader struct {
	db      *sql.DB
	decoder *zstd.Decoder
	scratch []byte
}
type blockRow struct {
	position
	data []byte
}

func openMap(ctx context.Context, path string) (*mapReader, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	stat, err := os.Stat(absolute)
	if err != nil {
		return nil, err
	}
	if !stat.Mode().IsRegular() {
		return nil, errors.New("map is not a regular file")
	}
	world, err := os.Open(filepath.Join(filepath.Dir(absolute), "world.mt"))
	if err == nil {
		defer world.Close()
		reader := bufio.NewScanner(world)
		for reader.Scan() {
			key, value, ok := strings.Cut(reader.Text(), "=")
			if !ok {
				continue
			}
			key = strings.TrimSpace(key)
			value = strings.TrimSpace(value)
			if key == "blocksize" && value != "16" {
				return nil, errors.New("only blocksize=16 is supported")
			}
			if key == "readonly_backend" && value != "" {
				return nil, errors.New("readonly_backend worlds require additional databases and are unsupported")
			}
		}
		if err = reader.Err(); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	uri := url.URL{Scheme: "file", Path: absolute}
	query := uri.Query()
	query.Set("mode", "ro")
	query.Add("_pragma", "query_only(1)")
	query.Add("_pragma", "busy_timeout(2000)")
	query.Add("_pragma", "trusted_schema(0)")
	uri.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	success := false
	defer func() {
		if !success {
			db.Close()
		}
	}()
	if err = db.PingContext(ctx); err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, "PRAGMA table_info(blocks)")
	if err != nil {
		return nil, err
	}
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, kind string
		var defaultValue any
		if err = rows.Scan(&cid, &name, &kind, &notnull, &defaultValue, &pk); err != nil {
			rows.Close()
			return nil, err
		}
		columns[name] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, name := range []string{"x", "y", "z", "data"} {
		if !columns[name] {
			return nil, errors.New("expected blocks(x,y,z,data) schema")
		}
	}
	decoder, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(maxMapBlock), zstd.WithDecoderMaxWindow(maxMapBlock), zstd.WithDecodeAllCapLimit(true))
	if err != nil {
		return nil, err
	}
	success = true
	return &mapReader{db: db, decoder: decoder, scratch: make([]byte, 0, maxMapBlock)}, nil
}
func (m *mapReader) close() { m.decoder.Close(); m.db.Close() }
func (m *mapReader) decode(row blockRow) (decodedBlock, error) {
	b, scratch, err := decodeBlock(m.decoder, row.data, m.scratch)
	m.scratch = scratch
	if err != nil {
		return b, fmt.Errorf("block (%d,%d,%d): %w", row.X, row.Y, row.Z, err)
	}
	return b, nil
}
func (m *mapReader) point(ctx context.Context, p position) (*blockRow, error) {
	row := blockRow{position: p}
	err := m.db.QueryRowContext(ctx, "SELECT data FROM blocks WHERE x=? AND y=? AND z=?", p.X, p.Y, p.Z).Scan(&row.data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &row, err
}
func (m *mapReader) batch(ctx context.Context, last *position) ([]blockRow, error) {
	query := "SELECT x,y,z,data FROM blocks"
	args := []any{}
	if last != nil {
		query += " WHERE (x,z,y) > (?,?,?)"
		args = append(args, last.X, last.Z, last.Y)
	}
	query += " ORDER BY x,z,y LIMIT 64"
	rows, err := m.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []blockRow{}
	for rows.Next() {
		var row blockRow
		if err = rows.Scan(&row.X, &row.Y, &row.Z, &row.data); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	// Rows close before CPU-heavy decoding; no long read transaction in DELETE mode.
	return result, rows.Err()
}

func observation(b decodedBlock, index int, at time.Time) mapObservation {
	name := b.names[b.nodes[index]]
	if name != "bones:bones" {
		return mapObservation{At: at, Present: false, Node: name, Access: "unknown"}
	}
	fields := b.metadata[index]
	owner, access := graveIdentity(fields)
	return mapObservation{At: at, Present: name == "bones:bones", Node: name, Owner: owner, Access: access, Metadata: fields}
}

func scanMap(ctx context.Context, path, mode string, s *graveStore, progress func(int, int, int)) (scanSummary, error) {
	summary := scanSummary{Mode: mode, StartedAt: time.Now()}
	reader, err := openMap(ctx, path)
	if err != nil {
		return summary, err
	}
	defer reader.close()
	known := map[position][]position{}
	seenPositions := map[position]bool{}
	for _, g := range s.Graves {
		p := g.pos()
		if !seenPositions[p] {
			known[blockOf(p)] = append(known[blockOf(p)], p)
			seenPositions[p] = true
		}
	}
	process := func(row *blockRow, bp position) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		at := time.Now()
		if row == nil {
			for _, p := range known[bp] {
				applyObservation(s, p, mapObservation{At: at, Present: false, Node: "missing_mapblock", Access: "unknown"})
				summary.Checked++
			}
			delete(known, bp)
			progress(summary.Blocks, summary.Found, summary.Checked)
			return nil
		}
		b, err := reader.decode(*row)
		if err != nil {
			return err
		}
		summary.Blocks++
		for _, p := range known[bp] {
			applyObservation(s, p, observation(b, indexOf(p), at))
			summary.Checked++
		}
		if mode == "full" {
			for index, id := range b.nodes {
				if b.names[id] == "bones:bones" {
					p := nodePosition(bp, index)
					if !seenPositions[p] {
						applyObservation(s, p, observation(b, index, at))
						seenPositions[p] = true
					}
					summary.Found++
				}
			}
		}
		delete(known, bp)
		progress(summary.Blocks, summary.Found, summary.Checked)
		return nil
	}
	if mode == "full" {
		var last *position
		for {
			if err = ctx.Err(); err != nil {
				return summary, err
			}
			batch, err := reader.batch(ctx, last)
			if err != nil {
				return summary, err
			}
			if len(batch) == 0 {
				break
			}
			for _, row := range batch {
				if err = process(&row, row.position); err != nil {
					return summary, err
				}
			}
			p := batch[len(batch)-1].position
			last = &p
		}
	}
	// Point lookups also handle known blocks missing from a full traversal.
	blocks := make([]position, 0, len(known))
	for p := range known {
		blocks = append(blocks, p)
	}
	sort.Slice(blocks, func(i, j int) bool {
		a, b := blocks[i], blocks[j]
		if a.X != b.X {
			return a.X < b.X
		}
		if a.Z != b.Z {
			return a.Z < b.Z
		}
		return a.Y < b.Y
	})
	for _, p := range blocks {
		if err = ctx.Err(); err != nil {
			return summary, err
		}
		row, err := reader.point(ctx, p)
		if err != nil {
			return summary, err
		}
		if err = process(row, p); err != nil {
			return summary, err
		}
	}
	if err = ctx.Err(); err != nil {
		return summary, err
	}
	deriveStore(s)
	summary.FinishedAt = time.Now()
	return summary, nil
}
