// Package messagehistory adapts Codex's shared composer history, independently
// of thread rollouts and app-server execution.
package messagehistory

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

const readLimit = 8 << 20

// Config is the flattened history section returned by config/read.
type Config struct {
	Persistence string `json:"persistence"`
	MaxBytes    uint64 `json:"max_bytes"`
}

type Store struct {
	Directory string
	Config    Config
}

type Entry struct {
	SessionID string `json:"session_id"`
	Timestamp uint64 `json:"ts"`
	Text      string `json:"text"`
}

// Directory resolves the actual child environment, including relative CODEX_HOME
// against the child's working directory, never the adapter's working directory.
func Directory(environment []string, cwd string) (string, error) {
	env := make(map[string]string)
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			if runtime.GOOS == "windows" {
				key = strings.ToUpper(key)
			}
			env[key] = value
		}
	}
	directory := env["CODEX_HOME"]
	if directory == "" {
		key := "HOME"
		if runtime.GOOS == "windows" {
			key = "USERPROFILE"
		}
		userDirectory := env[key]
		if userDirectory == "" {
			account, err := user.Current()
			if err != nil {
				return "", errors.New("Codex home is unavailable in the selected environment")
			}
			userDirectory = account.HomeDir
		}
		directory = filepath.Join(userDirectory, ".codex")
	}
	if !filepath.IsAbs(directory) {
		if !filepath.IsAbs(cwd) {
			return "", errors.New("relative Codex home requires an absolute process workspace")
		}
		directory = filepath.Join(cwd, directory)
	}
	return filepath.Clean(directory), nil
}

func (s Store) enabled() bool { return s.Config.Persistence != "none" }

func (s Store) open(ctx context.Context, write bool) (*os.File, error) {
	if s.Directory == "" || !filepath.IsAbs(s.Directory) {
		return nil, errors.New("Codex history directory is unavailable")
	}
	flags := os.O_RDONLY
	if write {
		if err := os.MkdirAll(s.Directory, 0700); err != nil {
			return nil, err
		}
		flags = os.O_RDWR | os.O_CREATE
	}
	f, err := openFile(filepath.Join(s.Directory, "history.jsonl"), flags)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = errors.New("Codex history is not a regular file")
	}
	if err == nil && write {
		err = f.Chmod(0600)
	}
	if err == nil {
		// Source: codex-rs/message-history/src/lib.rs:164:184@0df4aa677.
		// The stock writer bounds lock contention to ten 100ms attempts.
		var locked bool
		for range 10 {
			locked, err = tryLock(f, write)
			if locked || err != nil {
				break
			}
			select {
			case <-ctx.Done():
				err = ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
			if err != nil {
				break
			}
		}
		if err == nil && !locked {
			err = errors.New("Codex history file is busy")
		}
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// Read returns the newest 100 complete plain-text entries within a bounded tail.
// Corrupt complete records are reported while preserving available entries.
func (s Store) Read(ctx context.Context) ([]Entry, error) {
	f, err := s.open(ctx, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	offset := max(int64(0), info.Size()-readLimit)
	data := make([]byte, info.Size()-offset)
	if _, err := f.ReadAt(data, offset); err != nil && err != io.EOF {
		return nil, err
	}
	if offset > 0 {
		if end := bytes.IndexByte(data, '\n'); end >= 0 {
			data = data[end+1:]
		} else {
			return nil, errors.New("latest Codex history entry exceeds the read limit")
		}
	}
	lines := bytes.Split(data, []byte{'\n'})
	lines = lines[:len(lines)-1] // An incomplete append is not a record.
	var entries []Entry
	var malformed bool
	for _, line := range lines[max(0, len(lines)-100):] {
		var entry Entry
		if json.Unmarshal(line, &entry) != nil {
			malformed = true
			continue
		}
		if entry.Text != "" {
			entries = append(entries, entry)
		}
	}
	if malformed {
		return entries, errors.New("some Codex history entries could not be read")
	}
	return entries, nil
}

// Append shares the stock file lock, JSONL schema, private permissions and
// max_bytes soft-cap policy. It never writes thread rollouts.
// Source: codex-rs/message-history/src/lib.rs:102:277@0df4aa677.
func (s Store) Append(ctx context.Context, thread, text string) error {
	if !s.enabled() || strings.TrimSpace(text) == "" {
		return nil
	}
	if thread == "" {
		return errors.New("Codex history requires an established session")
	}
	entry := Entry{SessionID: thread, Timestamp: uint64(time.Now().Unix()), Text: text}
	line, err := json.Marshal(&entry)
	if err != nil {
		return errors.New("Codex history text could not be encoded")
	}
	line = append(line, '\n')
	f, err := s.open(ctx, true)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = f.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	if _, err = f.Write(line); err != nil {
		return err
	}
	if s.Config.MaxBytes == 0 {
		return nil
	}
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if uint64(info.Size()) <= s.Config.MaxBytes {
		return nil
	}
	target := max(s.Config.MaxBytes/5*4+(s.Config.MaxBytes%5)*4/5, uint64(len(line)))
	// Retain only a bounded suffix; a very large configured cap need not cause an
	// unbounded allocation. Earlier records are located by scanning backwards.
	start := info.Size()
	retained := int64(0)
	for start > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		n := min(int64(64<<10), start)
		block := make([]byte, n)
		if _, err := f.ReadAt(block, start-n); err != nil {
			return err
		}
		for i, b := range slices.Backward(block) {
			if b == '\n' && start-n+int64(i)+1 < info.Size() {
				size := info.Size() - (start - n + int64(i) + 1)
				if uint64(size) > target {
					start = start - n + int64(i) + 1
					goto found
				}
				retained = size
			}
		}
		start -= n
	}
found:
	if start == 0 && uint64(info.Size()) <= target {
		return nil
	}
	// At least the newest line survives even when it exceeds the configured cap.
	retained = max(retained, int64(len(line)))
	offset := info.Size() - retained
	if _, err := io.CopyN(io.NewOffsetWriter(f, 0), io.NewSectionReader(f, offset, retained), retained); err != nil {
		return err
	}
	if err := f.Truncate(retained); err != nil {
		return fmt.Errorf("trim Codex history: %w", err)
	}
	return nil
}
