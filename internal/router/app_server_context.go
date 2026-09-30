package router

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"io"
	"iter"
	"os"
	"path/filepath"
)

// Restore only observational context from the host-selected rollout. Reading a
// child's history must not attach to or resume it. Live usage always wins.
func restoreContextUsage(agent *activityPaneAgent, info appServerThreadInfo) {
	if agent == nil || agent.ContextKnown {
		return
	}
	for line := range reverseThreadRolloutRecords(info) {
		var event struct {
			Type    string `json:"type"`
			Payload struct {
				Type string `json:"type"`
				Info *struct {
					Last *struct {
						Total uint64 `json:"total_tokens"`
					} `json:"last_token_usage"`
					Window uint64 `json:"model_context_window"`
				} `json:"info"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &event) != nil || event.Type != "event_msg" || event.Payload.Type != "token_count" || event.Payload.Info == nil || event.Payload.Info.Last == nil {
			continue
		}
		agent.ContextTokens, agent.ContextWindow, agent.ContextKnown = event.Payload.Info.Last.Total, event.Payload.Info.Window, true
		return
	}
}

// Bound each observational read independently of transcript size and ignore
// boundary fragments. Missing evidence never becomes a fabricated snapshot.
func reverseThreadRolloutRecords(info appServerThreadInfo) iter.Seq[[]byte] {
	return func(yield func([]byte) bool) {
		f, stat, ok := openThreadRollout(info)
		if !ok {
			return
		}
		defer f.Close()
		const tailLimit = 8 << 20
		start := max(int64(0), stat.Size()-tailLimit)
		data, err := io.ReadAll(io.NewSectionReader(f, start, stat.Size()-start))
		if err != nil {
			return
		}
		if start > 0 {
			_, data, _ = bytes.Cut(data, []byte{'\n'})
		}
		end := bytes.LastIndexByte(data, '\n')
		if end < 0 {
			return
		}
		data = data[:end]
		for len(data) > 0 {
			previous := bytes.LastIndexByte(data, '\n')
			line := data[previous+1:]
			data = data[:max(0, previous)]
			if !yield(line) {
				return
			}
		}
	}
}

// openThreadRollout opens the host-selected rollout only when its session
// metadata names the thread; a mismatched or irregular file is not evidence.
func openThreadRollout(info appServerThreadInfo) (*os.File, os.FileInfo, bool) {
	if !filepath.IsAbs(info.Path) {
		return nil, nil, false
	}
	f, err := os.Open(info.Path)
	if err != nil {
		return nil, nil, false
	}
	stat, err := f.Stat()
	if err != nil || !stat.Mode().IsRegular() {
		f.Close()
		return nil, nil, false
	}
	var meta struct {
		Type    string `json:"type"`
		Payload struct {
			ID string `json:"id"`
		} `json:"payload"`
	}
	if json.UnmarshalDecode(jsontext.NewDecoder(io.LimitReader(f, 64<<10)), &meta) != nil || meta.Type != "session_meta" || meta.Payload.ID != info.ID {
		f.Close()
		return nil, nil, false
	}
	return f, stat, true
}
