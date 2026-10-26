package segments

// Source: apps/mekugi/internal/router/exec_track.go:29:296,562:618@b27b4c66.

import (
	"context"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/yusing/mekugi/modules/commandsegments/transport"
)

// Codex can wait 150 ms before reporting an early command exit. Stay below
// the helper's 500 ms deadline while allowing notification delivery time.
const claimWait = 350 * time.Millisecond

// Instrumentation contains authenticated guard directories, never transport paths.
type Instrumentation struct{ VCS, Sudo string }

// Observer correlates reports with host items. It neither runs commands nor
// owns the application scopes that subscribe to their observations.
type Observer struct {
	transport       *transport.Server
	instrumentation Instrumentation
	outputLimit     int
	mu              sync.Mutex
	started         []command
	changed         chan struct{}
	reports         map[[3]string]*report
	subscriptions   map[*Subscription]struct{}
	sequence        uint64
	closed          bool
}

type command struct {
	key    [3]string
	script string
	serial uint64
}

type report struct {
	Report
	serial  uint64
	pending [][]byte
}

// Report is a detached observation. HostOnly identifies a start awaiting a
// report. Ended includes transport failure; Closed and Done prove full closure.
type Report struct {
	Script                                         string
	Segments                                       []Segment
	Created                                        []transport.Message
	HostOnly, Terminal, Lossy, Ended, Closed, Done bool
	Code                                           int
	Version                                        uint64
}

type Segment struct {
	Timing       transport.Timing
	Source       string
	Began, Ended bool
	Code         int
	Output       string // Newly drained bytes, available only from Read.
}

func (r Report) Complete() bool { return r.Ended && r.Closed && r.Done }

// LastStarted distinguishes short-circuited operands from future commands.
func (r Report) LastStarted() int {
	for i, v := range slices.Backward(r.Segments) {
		if v.Began || v.Ended {
			return i
		}
	}
	return -1
}

func NewObserver(server *transport.Server, instrumentation Instrumentation, outputLimit int) (*Observer, error) {
	h := &Observer{instrumentation: instrumentation, outputLimit: outputLimit, changed: make(chan struct{}), reports: make(map[[3]string]*report)}
	if err := server.Register(h.claim); err != nil {
		return nil, err
	}
	h.transport = server
	return h, nil
}

func (h *Observer) Close() {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.closed = true
	h.mu.Unlock()
	h.transport.Close()
}

// Start receives the exact executing script after backend shell decoding.
// Repeated delivery before and during presentation is idempotent.
func (h *Observer) Start(key [3]string, script string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if slices.ContainsFunc(h.started, func(c command) bool { return c.key == key }) || h.reports[key] != nil {
		return
	}
	h.sequence++
	h.started = append(h.started, command{key: key, script: script, serial: h.sequence})
	for subscription := range h.subscriptions {
		subscription.retain(key, &report{Script: script, HostOnly: true, serial: h.sequence})
	}
	h.signal()
}

// Forget retires presentation's report. Existing subscriptions retain its facts.
func (h *Observer) Forget(key [3]string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.started = slices.DeleteFunc(h.started, func(c command) bool { return c.key == key })
	delete(h.reports, key)
}

// Completed records the host boundary independently of report or UI drainage.
func (h *Observer) Completed(key [3]string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for subscription := range h.subscriptions {
		for _, match := range subscription.matches {
			if match.key == key {
				match.hostCompleted = true
			}
		}
	}
	h.signal()
}

func (h *Observer) Tracking(key [3]string) bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.reports[key] != nil
}

// ReleaseOutput records that the presentation owner discarded earlier bytes.
// Subsequent attachments can retain status, but cannot claim a complete stream.
func (h *Observer) ReleaseOutput(key [3]string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if report := h.reports[key]; report != nil {
		report.Lossy = true
		clear(report.pending)
		report.Version++
	}
}

// Read drains pending bytes once into the application's Activity retention.
// Metadata snapshots used by capture subscriptions never consume output.
func (h *Observer) Read(key [3]string) (Report, bool) {
	if h == nil {
		return Report{}, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.reports[key]
	if r == nil {
		return Report{}, false
	}
	view := r.snapshot()
	for i := range view.Segments {
		view.Segments[i].Output = string(r.pending[i])
		r.pending[i] = nil
	}
	return view, true
}

func (r *report) snapshot() Report {
	view := r.Report
	view.Segments = slices.Clone(r.Segments)
	view.Created = slices.Clone(r.Created)
	return view
}

func (h *Observer) claim(ctx context.Context, hello transport.Message) transport.Report {
	timer := time.NewTimer(claimWait)
	defer timer.Stop()
	for {
		h.mu.Lock()
		if h.closed {
			h.mu.Unlock()
			return nil
		}
		index := -1
		for i, command := range h.started {
			if hello.Thread != "" && command.key[0] != hello.Thread {
				continue
			}
			// A guarded shell can add PATH prefixes after the host observed
			// its argv, including when the native pre-tool hook did not run.
			// Preserve the exact fast path and never normalize authored text.
			if command.script != hello.Script && h.source(command.script, command.key[2]) != h.source(hello.Script, command.key[2]) {
				continue
			}
			if index >= 0 {
				h.mu.Unlock()
				return nil
			}
			index = i
		}
		if index >= 0 {
			command := h.started[index]
			h.started = slices.Delete(h.started, index, index+1)
			r := &report{Script: hello.Script, Terminal: hello.Terminal, Version: 1, serial: command.serial, pending: make([][]byte, len(hello.Segments))}
			for _, source := range hello.Segments {
				r.Segments = append(r.Segments, Segment{Source: source})
			}
			h.reports[command.key] = r
			for subscription := range h.subscriptions {
				subscription.retain(command.key, r)
			}
			h.signal()
			h.mu.Unlock()
			return &stream{observer: h, key: command.key, report: r}
		}
		changed := h.changed
		h.mu.Unlock()
		select {
		case <-changed:
		case <-timer.C:
			return nil
		case <-ctx.Done():
			return nil
		}
	}
}

type stream struct {
	observer *Observer
	key      [3]string
	report   *report
}

func (s *stream) Observe(message transport.Message) {
	h := s.observer
	h.mu.Lock()
	defer h.mu.Unlock()
	r := s.report
	valid := message.Index >= 0 && message.Index < len(r.Segments)
	switch message.Type {
	case transport.Created:
		if valid && filepath.IsAbs(message.Data) {
			r.Created = append(r.Created, message)
		}
	case transport.Begin:
		if valid {
			r.Segments[message.Index].Began = true
			r.Segments[message.Index].Timing = transport.Timing{Started: message.Timing.Started}
		}
	case transport.Output:
		if valid && !r.Lossy {
			pending := &r.pending[message.Index]
			if len(*pending)+len(message.Data) > h.outputLimit {
				clear(r.pending)
				r.Lossy = true
			} else {
				*pending = append(*pending, message.Data...)
			}
		}
	case transport.End:
		if valid && message.Code != nil {
			segment := &r.Segments[message.Index]
			segment.Began, segment.Ended, segment.Code = true, true, *message.Code
			if !message.Timing.Started.IsZero() && message.Timing.Started.Equal(segment.Timing.Started) && !message.Timing.Ended.IsZero() && message.Timing.ElapsedNS >= 0 {
				segment.Timing = message.Timing
			}
		}
	case transport.Done:
		if message.Code != nil {
			r.Done, r.Code = true, *message.Code
		}
	case transport.Closed:
		r.Closed = true
	case transport.Lossy:
		r.Lossy = true
	default:
		return
	}
	r.Version++
	if message.Type != transport.Output {
		h.signal()
	}
}

func (s *stream) End() {
	s.observer.mu.Lock()
	defer s.observer.mu.Unlock()
	s.report.Ended = true
	s.report.Version++
	s.observer.signal()
}

func (s *stream) Abort() { s.observer.Forget(s.key) }

func (h *Observer) signal() { close(h.changed); h.changed = make(chan struct{}) }
