package diagnostic

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Each process retains the latest two stderr segments, at most 1 MiB total.
const processSegmentBytes = 512 << 10

// ProcessEvent records process facts independently of session/request diagnostics.
type ProcessEvent struct {
	Event          string    `json:"event"`
	Time           time.Time `json:"time"`
	PID            int       `json:"pid"`
	Socket         string    `json:"socket"`
	Executable     string    `json:"executable"`
	Build          string    `json:"build"`
	Revision       string    `json:"revision,omitempty"`
	Modified       bool      `json:"modified,omitzero"`
	GoVersion      string    `json:"go_version"`
	ExitCode       *int      `json:"exit_code,omitempty"`
	Signal         string    `json:"signal,omitempty"`
	Error          string    `json:"error,omitempty"`
	StderrError    string    `json:"stderr_error,omitempty"`
	RetentionError string    `json:"retention_error,omitempty"`
	StderrBytes    int64     `json:"stderr_bytes"`
}

// ProcessLog belongs to the detached supervisor, never to an interactive client.
// Its sole writer drains stderr even after a storage error, preserving execution.
type ProcessLog struct {
	mu           sync.Mutex
	directory    string
	release      func() error
	events       *os.File
	stderr       *os.File
	size         int
	total        int64
	err          error
	retentionErr error
}

func OpenProcessLog(ctx context.Context, root string) (*ProcessLog, error) {
	// Maintenance failures must not suppress new evidence in writable storage.
	retentionErr := new(Service).CleanupBundles(ctx, root, time.Now())
	directory, release, err := createBundle(root)
	if err != nil {
		return nil, err
	}
	p := &ProcessLog{directory: directory, release: release, retentionErr: retentionErr}
	p.events, err = os.OpenFile(filepath.Join(directory, "process.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err == nil {
		p.stderr, err = os.OpenFile(filepath.Join(directory, "stderr.log"), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	}
	if err != nil {
		return nil, errors.Join(err, p.Close())
	}
	return p, nil
}

func (p *ProcessLog) Record(event ProcessEvent) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	event.StderrBytes = p.total
	if p.err != nil {
		event.StderrError = p.err.Error()
	}
	if p.retentionErr != nil {
		event.RetentionError = p.retentionErr.Error()
	}
	data, err := json.Marshal(&event)
	if err != nil {
		return err
	}
	_, err = p.events.Write(append(data, '\n'))
	return err
}

func (p *ProcessLog) Write(data []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	count := len(data)
	p.total += int64(count)
	for len(data) > 0 && p.err == nil {
		if p.size == processSegmentBytes {
			p.err = p.stderr.Close()
			if p.err == nil {
				p.err = os.Rename(filepath.Join(p.directory, "stderr.log"), filepath.Join(p.directory, "stderr.previous.log"))
			}
			if p.err == nil {
				p.stderr, p.err = os.OpenFile(filepath.Join(p.directory, "stderr.log"), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
			}
			p.size = 0
			if p.err != nil {
				break
			}
		}
		n := min(len(data), processSegmentBytes-p.size)
		written, err := p.stderr.Write(data[:n])
		p.size += written
		p.err = err
		data = data[written:]
	}
	return count, nil
}

func (p *ProcessLog) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	var stderrErr, eventsErr error
	if p.stderr != nil {
		stderrErr = p.stderr.Close()
	}
	if p.events != nil {
		eventsErr = p.events.Close()
	}
	return errors.Join(p.err, stderrErr, eventsErr, p.release())
}
