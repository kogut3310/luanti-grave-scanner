package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type refreshResponse struct {
	Mode     string `json:"mode"`
	Added    int    `json:"added"`
	Total    int    `json:"total"`
	Replayed bool   `json:"replayed"`
}

var timestampPrefix = regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}`)

func anchor(file *os.File, offset int64) (int64, string, error) {
	start := offset - 4096
	if start < 0 {
		start = 0
	}
	b := make([]byte, offset-start)
	if _, err := file.ReadAt(b, start); err != nil && !(len(b) == 0 && err == io.EOF) {
		return 0, "", err
	}
	return start, fmt.Sprintf("%x", sha256.Sum256(b)), nil
}

func (a *App) refreshLogs(ctx context.Context, full bool) (refreshResponse, error) {
	if !a.scanMu.TryLock() {
		return refreshResponse{}, errBusy
	}
	defer a.scanMu.Unlock()
	return a.refreshLogsLocked(ctx, full)
}

func (a *App) refreshLogsLocked(ctx context.Context, full bool) (refreshResponse, error) {
	s := a.snapshot()
	file, err := os.Open(a.logPath)
	if err != nil {
		return refreshResponse{}, fmt.Errorf("open log: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return refreshResponse{}, err
	}
	if !info.Mode().IsRegular() {
		return refreshResponse{}, errors.New("log is not a regular file")
	}
	path, err := filepath.Abs(a.logPath)
	if err != nil {
		return refreshResponse{}, err
	}
	cursor := s.Cursor
	offset := cursor.Offset
	replay := full
	if offset < 0 || offset > info.Size() || cursor.FilePath != path || cursor.AnchorHash == "" {
		replay = true
	}
	if !replay {
		start, hash, err := anchor(file, offset)
		if err != nil {
			return refreshResponse{}, err
		}
		if start != cursor.AnchorStart || hash != cursor.AnchorHash {
			replay = true
		}
	}
	if replay {
		offset = 0
		cursor = scannerState{}
	}
	initialStart, initialHash, err := anchor(file, offset)
	if err != nil {
		return refreshResponse{}, err
	}
	if _, err = file.Seek(offset, io.SeekStart); err != nil {
		return refreshResponse{}, err
	}
	// Bound the read to the initial file size; leave partial trailing lines untouched.
	reader := bufio.NewReader(io.LimitReader(file, info.Size()-offset))
	tail := make([]byte, offset-initialStart)
	if _, err = file.ReadAt(tail, initialStart); err != nil && len(tail) > 0 {
		return refreshResponse{}, err
	}
	events := []storedLogEvent{}
	next := offset
	for {
		if err = ctx.Err(); err != nil {
			return refreshResponse{}, err
		}
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return refreshResponse{}, readErr
		}
		next += int64(len(line))
		if len(line) >= 4096 {
			tail = []byte(line[len(line)-4096:])
		} else {
			tail = append(tail, []byte(line)...)
			if len(tail) > 4096 {
				tail = tail[len(tail)-4096:]
			}
		}
		text := strings.TrimRight(line, "\r\n")
		if ts := timestampPrefix.FindString(text); ts != "" {
			cursor.LastTimestamp = ts
		}
		var e storedLogEvent
		if d, ok := parseDeathEvent(text); ok {
			e = storedLogEvent{Kind: "death", Timestamp: d.Timestamp, Player: d.Player, position: d.pos(), RawLine: text}
		} else if c, ok := parseCollectionEvent(text); ok {
			e = storedLogEvent{Kind: "collection", Timestamp: c.Timestamp, Player: c.Player, position: position{c.X, c.Y, c.Z}, RawLine: text}
		} else {
			continue
		}
		// Log text has no timezone; interpret it in the configured server timezone.
		e.Timestamp, err = time.ParseInLocation("2006-01-02 15:04:05", text[:19], a.logLocation)
		if err != nil {
			return refreshResponse{}, err
		}
		e.ID = eventID(e)
		events = append(events, e)
	}
	// Detect replacement/truncation during a manual scan instead of committing a mixed cursor.
	current, err := os.Stat(a.logPath)
	if err != nil {
		return refreshResponse{}, err
	}
	start, hash, err := anchor(file, offset)
	if err != nil {
		return refreshResponse{}, fmt.Errorf("log changed during scan: %w", err)
	}
	if !os.SameFile(info, current) || current.Size() < next || start != initialStart || hash != initialHash {
		return refreshResponse{}, errors.New("log changed during scan; retry refresh")
	}
	cursor.AnchorStart, cursor.AnchorHash, err = anchor(file, next)
	if err != nil {
		return refreshResponse{}, err
	}
	if cursor.AnchorHash != fmt.Sprintf("%x", sha256.Sum256(tail)) {
		return refreshResponse{}, errors.New("log contents changed during scan; retry refresh")
	}
	cursor.Offset = next
	cursor.FilePath = path
	added := mergeLogEvents(&s, events)
	s.Cursor = cursor
	if err = ctx.Err(); err != nil {
		return refreshResponse{}, err
	}
	if err = a.commit(s); err != nil {
		return refreshResponse{}, fmt.Errorf("save log results: %w", err)
	}
	mode := "incremental"
	if full {
		mode = "full"
	}
	return refreshResponse{Mode: mode, Added: added, Total: len(s.Graves), Replayed: replay}, nil
}
