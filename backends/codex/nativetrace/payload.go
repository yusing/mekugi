package nativetrace

import (
	"encoding/binary"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/klauspost/compress/zstd"
)

// payloadReader caches one shard for a single event-log refresh. A new event
// can reference a record appended since that shard was read.
type payloadReader struct {
	directory string
	shard     uint64
	size      int64
	records   map[string][]byte
}

var errPayloadNotCaptured = errors.New("native trace payload not fully captured")

func (p *payloadReader) read(version int, ref payloadRef, value any) error {
	if version < 3 {
		return readPayloadFile(p.directory, ref, value)
	}
	// The runtime only splits model-facing bodies. Invocation, dispatch
	// result and runtime result evidence must be a complete single record.
	if ref.RawID == "" || len(ref.Parts) != 0 {
		return errors.New("invalid native trace shard reference")
	}
	status := ref.Capture.Status
	if (status == "omitted" && ref.Shard == 0) || (status == "truncated" && ref.Shard != 0) {
		return errPayloadNotCaptured
	}
	if status != "" || ref.Shard == 0 {
		return errors.New("invalid native trace shard capture status")
	}
	if p.shard == ref.Shard {
		if body, ok := p.records[ref.RawID]; ok {
			return json.Unmarshal(body, value)
		}
	}
	if err := p.loadShard(ref.Shard); err != nil {
		return err
	}
	body, ok := p.records[ref.RawID]
	if !ok {
		return errors.New("native trace payload absent from shard")
	}
	return json.Unmarshal(body, value)
}

// Source: codex-rs/rollout-trace/src/shard.rs@[f6e03d1891].
// Each record is a u16 ID length, u64 body length, UTF-8 ID and exact JSON body.
const (
	shardHeaderSize = 10
	shardLimit      = payloadLimit + shardHeaderSize + (1<<16 - 1)
)

func (p *payloadReader) loadShard(number uint64) error {
	root, err := os.OpenRoot(p.directory)
	if err != nil {
		return err
	}
	defer root.Close()
	name := filepath.Join("payloads", fmt.Sprintf("%06d.zst", number))
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > shardLimit+(1<<20) {
		return errors.New("native trace shard unavailable or too large")
	}
	if p.shard == number && p.size == info.Size() {
		return nil
	}
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	// Native append flushes each record before publishing the event that
	// references it. Read the file prefix that existed at the size check.
	input := &shardInput{Reader: io.NewSectionReader(file, 0, info.Size())}
	decoder, err := zstd.NewReader(input,
		zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxWindow(8<<20))
	if err != nil {
		return err
	}
	defer decoder.Close()
	body, err := io.ReadAll(io.LimitReader(decoder, shardLimit+1))
	// The decoder can also report UnexpectedEOF *inside* a corrupt block.
	unsealed := input.eof && errors.Is(err, io.ErrUnexpectedEOF)
	if err != nil && !unsealed {
		return err
	}
	if len(body) > shardLimit {
		return errors.New("native trace decoded shard too large")
	}
	records := make(map[string][]byte)
	for len(body) >= shardHeaderSize {
		idSize := int(binary.LittleEndian.Uint16(body[:2]))
		bodySize := binary.LittleEndian.Uint64(body[2:shardHeaderSize])
		start := shardHeaderSize + idSize
		if start > len(body) || bodySize > uint64(len(body)-start) {
			break
		}
		if idSize == 0 || bodySize > payloadLimit || !utf8.Valid(body[shardHeaderSize:start]) {
			return errors.New("invalid native trace shard record")
		}
		id := string(body[shardHeaderSize:start])
		if _, exists := records[id]; exists {
			return errors.New("duplicate native trace shard record")
		}
		end := start + int(bodySize)
		records[id] = body[start:end]
		body = body[end:]
	}
	if len(body) != 0 && !unsealed {
		return errors.New("partial record in sealed native trace shard")
	}
	p.shard, p.size, p.records = number, info.Size(), records
	return nil
}

type shardInput struct {
	io.Reader
	eof bool
}

func (r *shardInput) Read(b []byte) (int, error) {
	n, err := r.Reader.Read(b)
	r.eof = errors.Is(err, io.EOF)
	return n, err
}
