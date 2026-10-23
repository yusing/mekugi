package sessionapi

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"

	threadcontrol "github.com/yusing/mekugi/modules/threadcontrol/control"
)

func (s *Server) BindRequests(requests threadcontrol.Runtime) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = requests
}

func (h *Hub) Requests(thread string) (threadcontrol.Runtime, error) {
	h.mu.Lock()
	s := h.sessions[thread]
	if s == nil || s.runtime == nil || s.runtime.ctx.Err() != nil {
		h.mu.Unlock()
		return nil, errors.New("thread has no running native runtime")
	}
	server := s.server
	h.mu.Unlock()
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.requests == nil {
		return nil, errors.New("native runtime has no admitted request owner")
	}
	return server.requests, nil
}

// StartPrepared admits execution owned by an existing native runtime. Launch
// supplies frozen execution dependencies; Hub retains the single runtime registry
// and joins child lifetimes. No renderer or ordinary initial input is required.
var ErrPreparedNotStarted = errors.New("prepared runtime was not started")

func (h *Hub) StartPrepared(request context.Context, parent, resume, workspace string, run func(context.Context, *Server) error) (string, error) {
	info, err := os.Stat(workspace)
	if !filepath.IsAbs(workspace) || err != nil || !info.IsDir() || run == nil {
		return "", errors.Join(ErrPreparedNotStarted, errors.New("prepared runtime requires an existing absolute workspace and execution owner"))
	}
	h.mu.Lock()
	s := h.sessions[parent]
	if h.stopping || request.Err() != nil || s == nil || s.runtime == nil || s.runtime.ctx.Err() != nil {
		h.mu.Unlock()
		return "", errors.Join(ErrPreparedNotStarted, errors.New("parent native runtime is unavailable"))
	}
	if resume != "" {
		if h.sessions[resume] != nil {
			h.mu.Unlock()
			return "", errors.Join(ErrPreparedNotStarted, errThreadOwned)
		}
		for current := range h.running {
			if current.resume == resume {
				h.mu.Unlock()
				return "", errors.Join(ErrPreparedNotStarted, errThreadOwned)
			}
		}
	}
	ctx, cancel := context.WithCancel(s.runtime.ctx)
	m := &managedSession{parent: s.runtime, parentThread: parent, resume: resume, run: run, operation: rand.Text(), ctx: ctx, server: NewPreparedLive(h.launcher.PrepareInput), inputContext: ctx, inputWorkspace: workspace, cancel: cancel, done: make(chan struct{}), ready: make(chan string, 1)}
	m.server.activeThread = h.activeThread
	h.running[m] = struct{}{}
	h.wg.Add(1)
	h.mu.Unlock()
	go h.run(ctx, m, Start{Workspace: workspace})
	select {
	case id := <-m.ready:
		return id, nil
	case <-m.done:
		return "", errors.Join(errors.New("prepared runtime stopped before readiness"), m.err)
	case <-request.Done():
		return "", errors.Join(errors.New("prepared runtime creation outcome is unknown"), request.Err())
	}
}
