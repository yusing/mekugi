package sessionapi

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"encoding/json/jsontext"
	json "encoding/json/v2"

	changes "github.com/yusing/mekugi/modules/changehistory/presentation"
)

type requestTarget struct{ thread, workspace string }

var errThreadOwned = errors.New("That thread is already running.")

var (
	errRuntimeStopped = errors.New("runtime stopped before readiness")
	errRuntimeUnknown = errors.New("runtime creation outcome is unknown")
)

// Hub admits and projects server-owned runtimes. Host dispatch remains with
// Launcher and the session's existing event loop.
type Hub struct {
	familyProjection familyProjection
	ctx              context.Context
	launcher         Launcher
	mu               sync.Mutex
	stopping         bool
	sessions         map[string]*listedSession
	running          map[*managedSession]struct{}
	list             []byte
	viewers          map[chan []byte]struct{}
	wg               sync.WaitGroup
	connections      map[*rpcConnection]struct{}
	connectionWG     sync.WaitGroup
	preparations     chan struct{}
}
type listedSession struct {
	entry   listed
	server  *Server
	runtime *managedSession
}
type managedSession struct {
	parent         *managedSession
	parentThread   string
	run            func(context.Context, *Server) error
	operation      string
	resume         string
	ctx            context.Context
	server         *Server
	cancel         context.CancelFunc
	done           chan struct{}
	ready          chan string
	id             string
	err            error
	initial        *PreparedInput
	inputContext   context.Context
	inputWorkspace string
}

func NewHub(ctx context.Context, launcher Launcher) *Hub {
	h := &Hub{ctx: ctx, launcher: launcher, sessions: map[string]*listedSession{}, running: map[*managedSession]struct{}{}, viewers: map[chan []byte]struct{}{}, connections: map[*rpcConnection]struct{}{}, preparations: make(chan struct{}, preparationLimit)}
	h.publishList()
	return h
}

func (h *Hub) activeThread(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.sessions[id]
	return s != nil && s.server.live && s.runtime != nil && s.runtime.ctx.Err() == nil
}

func (h *Hub) publishList() {
	entries := make([]listed, 0, len(h.sessions))
	for _, s := range h.sessions {
		if s.entry.ParentThread == "" {
			entries = append(entries, s.entry)
		}
	}
	slices.SortFunc(entries, func(a, b listed) int { return cmp.Or(cmp.Compare(b.Started, a.Started), strings.Compare(a.ID, b.ID)) })
	data, err := json.Marshal(struct {
		Sessions []listed `json:"sessions"`
	}{entries}, encoding)
	if err != nil || bytes.Equal(data, h.list) {
		return
	}
	h.list = data
	for ch := range h.viewers {
		select {
		case <-ch:
		default:
		}
		ch <- data
	}
}

func (h *Hub) Handler() http.Handler {
	return (&rpcEndpoint{hub: h, preparations: h.preparations}).handler()
}

func (h *Hub) lookup(thread, startup string, authority viewAuthority) (*listedSession, *RPCError) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if startup != "" {
		if authority != localNativeView || thread != "" {
			return nil, rpcFailure(400, "Invalid startup target.")
		}
		for m := range h.running {
			if m.operation == startup {
				return &listedSession{entry: listed{Workspace: m.inputWorkspace}, server: m.server, runtime: m}, nil
			}
		}
		return nil, rpcFailure(404, "No such startup operation.")
	}
	if s := h.sessions[thread]; s != nil {
		return s, nil
	}
	return nil, rpcFailure(404, "No such thread; it may have ended.")
}

func (h *Hub) startOptions(ctx context.Context) (any, *RPCError) {
	if h.launcher == nil {
		return nil, rpcFailure(405, "This server does not start threads.")
	}
	options, err := h.launcher.StartOptions(ctx)
	if err != nil {
		return nil, rpcFailure(503, "Start options are unavailable.")
	}
	return options, nil
}

func (h *Hub) startSession(requestContext context.Context, body jsontext.Value) (any, *RPCError) {
	var start Start
	if err := decodeBody(body, &start); err != nil {
		return nil, err
	}
	m, _, err := h.admitRuntime(requestContext, nil, start, "", nil)
	if err != nil {
		return nil, runtimeStartFailure(err)
	}
	if len(start.Attachments) > 0 {
		stopCleanup := context.AfterFunc(requestContext, m.initial.Cleanup)
		defer stopCleanup()
	}
	id, err := m.waitReady(requestContext)
	if err != nil {
		return nil, runtimeStartFailure(err)
	}
	notice := ""
	if m.initial != nil {
		notice = m.initial.Draft.AttachmentNotice
	}
	return struct {
		ID     string `json:"id"`
		Notice string `json:"notice,omitempty"`
	}{id, notice}, nil
}

// admitRuntime owns construction and registration for every execution input.
// A native view is installed before execution; frozen preparation supplies the
// execution operation without changing runtime admission or publication.
func (h *Hub) admitRuntime(requestContext, viewContext context.Context, start Start, parent string, run func(context.Context, *Server) error) (*managedSession, *rpcSubscription, error) {
	if h.launcher == nil {
		return nil, nil, rpcFailure(405, "This server does not start threads.")
	}
	var prepared *PreparedInput
	requestedThread := start.Resume
	if start.Replay != nil {
		if start.Workspace != "" || start.Model != "" || start.Effort != "" || start.Text != "" || len(start.Attachments) != 0 || start.Resume != "" {
			return nil, nil, rpcFailure(400, "Recorded playback cannot include live session fields.")
		}
		replay := start.Replay
		if strings.TrimSpace(replay.Session) == "" || strings.ContainsAny(replay.Session, `/\*?[]`) || replay.Speed != nil && (math.IsNaN(*replay.Speed) || math.IsInf(*replay.Speed, 0) || *replay.Speed < 0.1 || *replay.Speed > 100) {
			return nil, nil, rpcFailure(400, "Choose a recorded session ID and a speed between 0.1 and 100.")
		}
		requestedThread = replay.Session
	} else {
		start.Text = strings.TrimSpace(start.Text)
		if strings.HasPrefix(start.Text, "/") || strings.HasPrefix(start.Text, "!") {
			return nil, nil, rpcFailure(400, "Initial input must be a message, not a slash or shell command.")
		}
		info, err := os.Stat(start.Workspace)
		if !filepath.IsAbs(start.Workspace) || err != nil || !info.IsDir() {
			return nil, nil, rpcFailure(400, "Choose an existing absolute workspace directory.")
		}
		start.Workspace = filepath.Clean(start.Workspace)
		if start.Native != nil {
			start.Native.Workspace = start.Workspace
		} else if run == nil {
			options, err := h.launcher.StartOptions(requestContext)
			if err != nil {
				return nil, nil, rpcFailure(503, "Start options are unavailable.")
			}
			if err = options.ValidateSelection(start.Model, start.Effort); err != nil {
				return nil, nil, rpcFailure(400, err.Error())
			}
		}
		if start.Text != "" || len(start.Attachments) > 0 {
			var failure *RPCError
			prepared, failure = acquireInput(h.preparations, func() (*PreparedInput, error) {
				return h.launcher.PrepareInput(requestContext, start.Workspace, InputRequest{Text: start.Text, Attachments: start.Attachments})
			})
			if failure != nil {
				return nil, nil, failure
			}
		}
	}
	scheduled := false
	defer func() {
		if !scheduled {
			prepared.Cleanup()
		}
	}()
	h.mu.Lock()
	if h.stopping || h.ctx.Err() != nil || requestContext.Err() != nil {
		h.mu.Unlock()
		return nil, nil, rpcFailure(503, "The server is stopping.")
	}
	var parentRuntime *managedSession
	lifetime := h.ctx
	if parent != "" {
		s := h.sessions[parent]
		if s == nil || s.runtime == nil || s.runtime.ctx.Err() != nil {
			h.mu.Unlock()
			return nil, nil, rpcFailure(409, "Parent native runtime is unavailable.")
		}
		parentRuntime = s.runtime
		lifetime = parentRuntime.ctx
	}
	resuming := h.sessions[requestedThread] != nil
	for current := range h.running {
		resuming = resuming || requestedThread != "" && current.resume == requestedThread
	}
	if requestedThread != "" && resuming {
		h.mu.Unlock()
		return nil, nil, errThreadOwned
	}
	ctx, cancel := context.WithCancel(lifetime)
	server := NewPreparedLive(h.launcher.PrepareInput)
	if start.Replay != nil {
		server = New()
	}
	if run == nil {
		run = func(ctx context.Context, server *Server) error { return h.launcher.Run(ctx, start, server) }
	}
	m := &managedSession{parent: parentRuntime, parentThread: parent, run: run, operation: rand.Text(), resume: requestedThread, ctx: ctx, server: server, initial: prepared, inputContext: ctx, inputWorkspace: start.Workspace, cancel: cancel, done: make(chan struct{}), ready: make(chan string, 1)}
	m.server.activeThread = h.activeThread
	m.server.family = func() (*Family, *changes.ReviewView) {
		if frame := m.server.frame.Load(); frame != nil {
			return h.Family(frame.Session.Thread)
		}
		return nil, nil
	}
	if len(start.Attachments) > 0 {
		m.inputContext = requestContext
	}
	var subscription *rpcSubscription
	var attach NativeRequest
	if viewContext != nil {
		var failure *RPCError
		subscription, attach, failure = server.Native().newView(viewContext, requestTarget{workspace: start.Workspace}, m.operation, localNativeView)
		if failure != nil {
			h.mu.Unlock()
			cancel()
			return nil, nil, failure
		}
	}
	h.running[m] = struct{}{}
	h.wg.Add(1)
	h.mu.Unlock()
	scheduled = true
	go h.run(ctx, m, start)
	if subscription != nil {
		attach.Context = requestContext
		if failure := server.Native().deliver(attach); failure != nil {
			subscription.close()
			return m, nil, failure
		}
	}
	return m, subscription, nil
}

func (m *managedSession) waitReady(requestContext context.Context) (string, error) {
	select {
	case id := <-m.ready:
		return id, nil
	case <-m.done:
		return "", errors.Join(errRuntimeStopped, m.err)
	case <-requestContext.Done():
		return "", errors.Join(errRuntimeUnknown, requestContext.Err())
	}
}

func runtimeStartFailure(err error) *RPCError {
	if errors.Is(err, errThreadOwned) {
		return rpcFailure(409, errThreadOwned.Error())
	}
	if errors.Is(err, errRuntimeUnknown) {
		return rpcUnknown("Creation outcome unknown; do not automatically retry.")
	}
	if !errors.Is(err, errRuntimeStopped) {
		if failure, ok := errors.AsType[*RPCError](err); ok {
			return failure
		}
	}
	return rpcFailure(502, "Thread startup failed.")
}

func (h *Hub) run(ctx context.Context, m *managedSession, start Start) {
	defer h.wg.Done()
	defer m.initial.Cleanup()
	m.server.reserveThread = func(thread string) error {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.stopping || ctx.Err() != nil {
			return context.Canceled
		}
		if thread != "" {
			if current := h.sessions[thread]; current != nil && current.runtime != m {
				return errThreadOwned
			}
			for current := range h.running {
				if current != m && current.resume == thread {
					return errThreadOwned
				}
			}
		}
		m.resume = thread
		return nil
	}
	result := make(chan error, 1)
	go func() {
		result <- m.run(ctx, m.server)
	}()
	defer func() {
		if m.server.live {
			m.server.Native().Finish(m.err)
		}
		m.cancel()
		h.mu.Lock()
		var children []<-chan struct{}
		for child := range h.running {
			if child.parent == m {
				children = append(children, child.done)
			}
		}
		h.mu.Unlock()
		for _, done := range children {
			<-done
		}
		h.mu.Lock()
		for id, s := range h.sessions {
			if s.runtime == m {
				delete(h.sessions, id)
			}
		}
		delete(h.running, m)
		h.publishList()
		h.mu.Unlock()
		close(m.done)
	}()
	var reply <-chan string
	var attemptedSummary []byte
	var inputThread string
	ready := false
	pending := start.Text != "" || len(start.Attachments) > 0
	var inputCanceled <-chan struct{}
	if len(start.Attachments) > 0 {
		inputCanceled = m.inputContext.Done()
	}
	for {
		select {
		case m.err = <-result:
			return
		case <-ctx.Done():
			m.cancel()
			m.err = <-result
			return
		case <-inputCanceled:
			inputCanceled = nil
			if !m.initial.Admitted() {
				m.cancel()
				m.err = <-result
				return
			}
		case refusal := <-reply:
			reply = nil
			if refusal != "" {
				// A newer summary may have arrived while this answer was
				// pending. Retry only that new state after an explicit refusal;
				// unchanged readiness and unknown outcomes never resubmit.
				m.server.mu.Lock()
				if !bytes.Equal(attemptedSummary, m.server.summary) {
					select {
					case m.server.summaryChanged <- struct{}{}:
					default:
					}
				}
				m.server.mu.Unlock()
				continue
			}
			pending = false
			inputCanceled = nil
			if !ready {
				m.ready <- m.id
				ready = true
			}
			h.mu.Lock()
			if s := h.sessions[m.id]; s != nil {
				next := *s
				next.entry.Starting = false
				h.sessions[m.id] = &next
				h.publishList()
			}
			h.mu.Unlock()
		case <-m.server.summaryChanged:
			m.server.mu.Lock()
			summary := m.server.identity
			summaryBytes := m.server.summary
			m.server.mu.Unlock()
			if summary.Thread == "" {
				continue
			}
			if pending {
				if inputThread == "" {
					inputThread = summary.Thread
				}
				if summary.Thread != inputThread || summary.Workspace != m.inputWorkspace {
					m.cancel()
					m.err = <-result
					return
				}
			}
			h.mu.Lock()
			conflict := false
			for current := range h.running {
				conflict = conflict || current != m && current.resume == summary.Thread
			}
			if current := h.sessions[summary.Thread]; conflict || current != nil && current.runtime != m {
				h.mu.Unlock()
				m.cancel()
				<-result
				m.err = errThreadOwned
				return
			}
			if m.id != "" && m.id != summary.Thread {
				delete(h.sessions, m.id)
			}
			m.id = summary.Thread
			if m.resume == summary.Thread {
				m.resume = ""
			}
			entry := projectSummary(summary)
			entry.ParentThread = m.parentThread
			entry.ID = m.id
			entry.Starting = !ready
			h.sessions[m.id] = &listedSession{entry: entry, server: m.server, runtime: m}
			h.publishList()
			h.mu.Unlock()
			if !summary.Ready || ready || reply != nil {
				continue
			}
			if !pending {
				h.mu.Lock()
				if current := h.sessions[m.id]; current != nil {
					next := *current
					next.entry.Starting = false
					h.sessions[m.id] = &next
					h.publishList()
				}
				h.mu.Unlock()
				m.ready <- m.id
				ready = true
				continue
			}
			answer := make(chan string, 1)
			intent := Intent{Action: Send, Text: start.Text, Prepared: m.initial, Thread: inputThread, Workspace: m.inputWorkspace, Context: m.inputContext, Reply: answer}
			select {
			case m.server.intents <- intent:
				reply = answer
				attemptedSummary = summaryBytes
			case m.err = <-result:
				return
			case <-inputCanceled:
				m.cancel()
				m.err = <-result
				return
			case <-ctx.Done():
				m.cancel()
				m.err = <-result
				return
			}
		}
	}
}

// Close cancels and joins only runtimes owned by this server.
func (h *Hub) Close() {
	h.mu.Lock()
	h.stopping = true
	for m := range h.running {
		m.cancel()
	}
	h.mu.Unlock()
	h.wg.Wait()
	h.mu.Lock()
	connections := make([]*rpcConnection, 0, len(h.connections))
	for connection := range h.connections {
		connections = append(connections, connection)
	}
	h.mu.Unlock()
	for _, connection := range connections {
		connection.beginDrain()
	}
	h.connectionWG.Wait()
}
