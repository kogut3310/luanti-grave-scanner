package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/klauspost/compress/zstd"
)

const blockSize = 16
const maxMapBlock = 16 << 20

type blockReader struct {
	data   []byte
	offset int
}

func (r *blockReader) take(n int) ([]byte, error) {
	if n < 0 || n > len(r.data)-r.offset {
		return nil, io.ErrUnexpectedEOF
	}
	b := r.data[r.offset : r.offset+n]
	r.offset += n
	return b, nil
}
func (r *blockReader) number(n int) (int, error) {
	b, err := r.take(n)
	if err != nil {
		return 0, err
	}
	switch n {
	case 1:
		return int(b[0]), nil
	case 2:
		return int(binary.BigEndian.Uint16(b)), nil
	case 4:
		return int(binary.BigEndian.Uint32(b)), nil
	}
	return 0, errors.New("invalid number width")
}
func (r *blockReader) str(width int) (string, error) {
	n, err := r.number(width)
	if err != nil {
		return "", err
	}
	b, err := r.take(n)
	return string(b), err
}
func (r *blockReader) inventory() error {
	for {
		tail := r.data[r.offset:]
		n := bytes.IndexByte(tail, '\n')
		if n < 0 {
			return errors.New("missing EndInventory")
		}
		r.offset += n + 1
		if string(bytes.TrimSuffix(tail[:n], []byte{'\r'})) == "EndInventory" {
			return nil
		}
	}
}

type decodedBlock struct {
	names    map[int]string
	nodes    []int
	metadata map[int]map[string]string
}

func floorBlock(value int) int {
	q := value / blockSize
	if value%blockSize < 0 {
		q--
	}
	return q
}
func blockOf(p position) position { return position{floorBlock(p.X), floorBlock(p.Y), floorBlock(p.Z)} }
func indexOf(p position) int {
	b := blockOf(p)
	return (p.X - b.X*16) + (p.Y-b.Y*16)*16 + (p.Z-b.Z*16)*256
}
func nodePosition(b position, index int) position {
	return position{b.X*16 + index%16, b.Y*16 + (index/16)%16, b.Z*16 + index/256}
}

func decodeBlock(decoder *zstd.Decoder, blob []byte, scratch []byte) (decodedBlock, []byte, error) {
	b := decodedBlock{names: map[int]string{}, metadata: map[int]map[string]string{}}
	if len(blob) == 0 || blob[0] != 29 {
		return b, scratch, errors.New("unsupported mapblock format (expected 29)")
	}
	if len(blob) > maxMapBlock {
		return b, scratch, errors.New("compressed mapblock exceeds limit")
	}
	data, err := decoder.DecodeAll(blob[1:], scratch[:0])
	if err != nil {
		return b, scratch, fmt.Errorf("zstd: %w", err)
	}
	scratch = data
	if len(data) > maxMapBlock {
		return b, scratch, errors.New("decompressed mapblock exceeds limit")
	}
	r := blockReader{data: data}
	if _, err = r.take(7); err != nil {
		return b, scratch, err
	} // flags, lighting, game-time timestamp
	version, err := r.number(1)
	if err != nil || version != 0 {
		return b, scratch, errors.New("unsupported name mapping")
	}
	count, err := r.number(2)
	if err != nil || count > 4096 {
		return b, scratch, errors.New("invalid name mapping count")
	}
	for n := 0; n < count; n++ {
		id, e := r.number(2)
		if e != nil {
			return b, scratch, e
		}
		name, e := r.str(2)
		if e != nil {
			return b, scratch, e
		}
		if _, exists := b.names[id]; exists {
			return b, scratch, errors.New("duplicate content ID")
		}
		b.names[id] = name
	}
	contentWidth, err := r.number(1)
	if err != nil || contentWidth != 2 {
		return b, scratch, errors.New("expected two-byte node content")
	}
	paramsWidth, err := r.number(1)
	if err != nil || paramsWidth != 2 {
		return b, scratch, errors.New("invalid parameter width")
	}
	b.nodes = make([]int, 4096)
	for i := range b.nodes {
		b.nodes[i], err = r.number(2)
		if err != nil {
			return b, scratch, err
		}
		if _, ok := b.names[b.nodes[i]]; !ok {
			return b, scratch, errors.New("content ID has no name mapping")
		}
	}
	if _, err = r.take(8192); err != nil {
		return b, scratch, err
	}
	version, err = r.number(1)
	if err != nil {
		return b, scratch, err
	}
	if version == 0 {
		return b, scratch, nil
	}
	if version != 2 {
		return b, scratch, errors.New("unsupported node metadata version (expected 0 or 2)")
	}
	count, err = r.number(2)
	if err != nil || count > 4096 {
		return b, scratch, errors.New("invalid metadata count")
	}
	for n := 0; n < count; n++ {
		index, e := r.number(2)
		if e != nil || index >= 4096 {
			return b, scratch, errors.New("invalid metadata position")
		}
		if _, ok := b.metadata[index]; ok {
			return b, scratch, errors.New("duplicate metadata position")
		}
		variables, e := r.number(4)
		if e != nil || variables > 4096 {
			return b, scratch, errors.New("invalid metadata variable count")
		}
		fields := map[string]string{}
		for j := 0; j < variables; j++ {
			key, e := r.str(2)
			if e != nil {
				return b, scratch, e
			}
			value, e := r.str(4)
			if e != nil {
				return b, scratch, e
			}
			if _, e = r.number(1); e != nil {
				return b, scratch, e
			}
			if key != "formspec" {
				fields[key] = value
			}
		}
		b.metadata[index] = fields
		if err = r.inventory(); err != nil {
			return b, scratch, err
		}
	}
	return b, scratch, nil
}

var translatedBones = regexp.MustCompile("^\x1b\\(T@bones\\)\x1bF([^\x1b]+)\x1bE's (fresh|old) bones\x1bE$")
var plainBones = regexp.MustCompile("^(.+)'s (fresh|old) bones$")

func graveIdentity(fields map[string]string) (string, string) {
	owner := strings.TrimSpace(fields["owner"])
	match := translatedBones.FindStringSubmatch(fields["infotext"])
	if match == nil {
		match = plainBones.FindStringSubmatch(fields["infotext"])
	}
	access := "unknown"
	if owner != "" {
		access = "private"
	} else if len(match) == 3 && match[2] == "old" {
		access = "public"
	}
	if owner == "" && len(match) == 3 {
		owner = match[1]
	}
	return owner, access
}
