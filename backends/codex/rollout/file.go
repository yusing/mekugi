package rollout

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"io"
	"iter"
	"os"
	"path/filepath"

	"github.com/yusing/mekugi/libraries/sourceanalysis/sourceparse"
)

// Source: internal/router/app_server_context.go:42:102@[bf0e408c] selected rollout reads.

// Bound each observational read independently of transcript size and ignore
// boundary fragments. Missing evidence never becomes a fabricated snapshot.
func reverseRecords(threadID, path string) iter.Seq[[]byte] {
	return func(yield func([]byte) bool) {
		f, stat, ok := Open(threadID, path)
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
		data, _, complete := bytes.CutLast(data, []byte{'\n'})
		if !complete {
			return
		}
		for len(data) > 0 {
			before, line, found := bytes.CutLast(data, []byte{'\n'})
			if !found {
				yield(data)
				return
			}
			data = before
			if !yield(line) {
				return
			}
		}
	}
}

// Open opens the host-selected rollout only when its session
// metadata names the thread; a mismatched or irregular file is not evidence.
func Open(threadID, path string) (*os.File, os.FileInfo, bool) {
	if !filepath.IsAbs(path) {
		return nil, nil, false
	}
	f, err := sourceparse.Open(path)
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
	if json.UnmarshalDecode(jsontext.NewDecoder(io.LimitReader(f, 64<<10)), &meta) != nil || meta.Type != "session_meta" || meta.Payload.ID != threadID {
		f.Close()
		return nil, nil, false
	}
	return f, stat, true
}
