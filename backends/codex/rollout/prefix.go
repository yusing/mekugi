package rollout

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
)

// PrefixSource is supplied by the invocation's admitted history locator. A
// rollout ID differs from its stable thread ID after a host revert.
type PrefixSource struct{ Thread, Rollout, Path string }

type PrefixResolver func(context.Context, string) (PrefixSource, error)

type PrefixBoundary struct{ Turn, Item string }

type historyPosition struct {
	Rollout string `json:"thread_id"`
	Ordinal uint64 `json:"end_ordinal_exclusive"`
	Bytes   int64  `json:"end_byte_offset"`
}

// prefixRecord decodes the fields used for admission once and retains every
// other host field verbatim. Ordinals belong to storage, not the legacy import.
type prefixRecord struct {
	Type    string  `json:"type"`
	Ordinal *uint64 `json:"ordinal,omitzero"`
	Payload struct {
		ID    string                    `json:"id,omitzero"`
		Mode  string                    `json:"history_mode,omitzero"`
		Base  *historyPosition          `json:"history_base,omitzero"`
		Turn  string                    `json:"turn_id,omitzero"`
		Type  string                    `json:"type,omitzero"`
		Call  string                    `json:"call_id,omitzero"`
		Other map[string]jsontext.Value `json:",embed"`
	} `json:"payload"`
	Other map[string]jsontext.Value `json:",embed"`
}

// ReadPrefix freezes the invoking execution's durable prefix without waiting for
// its active turn or modifying its source. Referenced pages become a private,
// self-contained legacy import, retaining their actual reverted rollout identity.
// Source: codex-rs/protocol/src/protocol.rs:3234 HistoryPosition and
// codex-rs/rollout/src/ordinal.rs@0df4aa677.
func ReadPrefix(ctx context.Context, source PrefixSource, boundary PrefixBoundary, resolve PrefixResolver) ([]byte, error) {
	if boundary.Turn == "" || boundary.Item == "" || source.Thread == "" || source.Rollout == "" {
		return nil, errors.New("fork prefix requires admitted source, turn and item identities")
	}
	reader := prefixReader{ctx: ctx, resolve: resolve, visiting: make(map[string]bool), remaining: maxInspectionBytes}
	header, rows, found, err := reader.read(source, nil, boundary)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New("invoking execution boundary is unavailable in admitted history")
	}
	header.Payload.Mode, header.Payload.Base = "legacy", nil
	delete(header.Payload.Other, "subagent_history_start_ordinal")
	var out bytes.Buffer
	encoder := jsontext.NewEncoder(&out)
	for _, record := range append([]prefixRecord{*header}, rows...) {
		record.Ordinal = nil
		if err := json.MarshalEncode(encoder, &record); err != nil {
			return nil, err
		}
		if out.Len() > maxInspectionBytes {
			return nil, errors.New("materialized fork prefix exceeds retained read limit")
		}
	}
	return out.Bytes(), nil
}

type prefixReader struct {
	ctx       context.Context
	resolve   PrefixResolver
	visiting  map[string]bool
	remaining int64
}

func (r *prefixReader) read(source PrefixSource, end *historyPosition, boundary PrefixBoundary) (*prefixRecord, []prefixRecord, bool, error) {
	if source.Thread == "" || source.Rollout == "" || r.visiting[source.Rollout] {
		return nil, nil, false, errors.New("missing or cyclic admitted rollout identity")
	}
	r.visiting[source.Rollout] = true
	defer delete(r.visiting, source.Rollout)
	f, stat, ok := Open(source.Thread, source.Path)
	if !ok {
		return nil, nil, false, fmt.Errorf("rollout %s is unavailable or has conflicting thread identity", source.Rollout)
	}
	defer f.Close()
	limit := stat.Size()
	if end != nil {
		if end.Rollout != source.Rollout || end.Bytes <= 0 || end.Bytes > limit {
			return nil, nil, false, errors.New("referenced rollout byte boundary is unavailable")
		}
		limit = end.Bytes
	}
	input := bufio.NewReader(io.NewSectionReader(f, 0, min(limit, r.remaining+1)))
	var header *prefixRecord
	var rows []prefixRecord
	var next uint64
	var paginated bool
	var turn string
	var consumed int64
	for {
		if err := r.ctx.Err(); err != nil {
			return nil, nil, false, err
		}
		line, err := input.ReadBytes('\n')
		if len(line) == 0 && err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, false, errors.New("fork prefix ends in an incomplete record")
		}
		consumed += int64(len(line))
		r.remaining -= int64(len(line))
		if r.remaining < 0 || len(line) > maxInspectionLineBytes {
			return nil, nil, false, errors.New("fork prefix exceeds admitted history read limit")
		}
		var record prefixRecord
		if err := json.Unmarshal(line, &record); err != nil {
			return nil, nil, false, err
		}
		meta := record.Payload
		if header == nil {
			if record.Type != "session_meta" || meta.ID != source.Thread {
				return nil, nil, false, errors.New("fork prefix metadata changed during capture")
			}
			if meta.Mode != "" && meta.Mode != "legacy" && meta.Mode != "paginated" {
				return nil, nil, false, errors.New("fork prefix has an unsupported host history mode")
			}
			header, paginated = &record, meta.Mode == "paginated"
			if meta.Base != nil {
				if !paginated || r.resolve == nil {
					return nil, nil, false, errors.New("referenced fork history requires its admitted rollout locator")
				}
				parent, err := r.resolve(r.ctx, meta.Base.Rollout)
				if err != nil {
					return nil, nil, false, err
				}
				_, rows, _, err = r.read(parent, meta.Base, PrefixBoundary{})
				if err != nil {
					return nil, nil, false, err
				}
				next = meta.Base.Ordinal
			}
		} else if record.Type == "session_meta" && meta.ID == "" {
			return nil, nil, false, errors.New("copied history has malformed ancestor identity")
		}
		if paginated {
			if record.Ordinal == nil || *record.Ordinal != next || next == ^uint64(0) {
				return nil, nil, false, errors.New("fork prefix has missing or discontinuous ordinal evidence")
			}
			next++
		}
		if record.Type != "session_meta" {
			rows = append(rows, record)
		}
		if end == nil && record.Type == "turn_context" {
			turn = meta.Turn
		}
		if end == nil && record.Type == "response_item" && meta.ID == boundary.Item && boundary.Item != "" {
			if turn != boundary.Turn || meta.Call == "" || meta.Type != "function_call" && meta.Type != "custom_tool_call" {
				return nil, nil, false, errors.New("invoking execution item conflicts with retained turn or call kind")
			}
			return header, rows, true, nil
		}
	}
	if header == nil {
		return nil, nil, false, errors.New("empty fork history")
	}
	if end != nil && (!paginated || consumed != end.Bytes || next != end.Ordinal) {
		return nil, nil, false, errors.New("referenced rollout ordinal and byte boundaries disagree")
	}
	return header, rows, false, nil
}
