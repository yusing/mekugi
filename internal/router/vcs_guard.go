package router

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/yusing/mekugi/backends/codex/binding"

	"github.com/yusing/mekugi/backends/codex/shellapproval"
)

// vcsApproval is one guarded remote write waiting for the user. The guard
// handler owns its lifetime: it closes done when the command stops waiting,
// after setting outcome, so the UI never answers a request nobody reads.
type vcsApproval struct {
	thread, cwd string
	kind        string
	item        string // Exact commandExecution item from the native hook.
	executable  string
	argv        []string
	reply       chan shellapproval.Reply // Buffered; the UI's answer.
	done        chan struct{}
	outcome     string // Why the request ended without the UI's answer.
}

func (a *vcsApproval) finished() bool {
	select {
	case <-a.done:
		return true
	default:
		return false
	}
}

// shellApprovalServer owns the shared VCS/sudo decision channel independently
// of command reports. Request completion and secret handling stay with callers.
type shellApprovalServer struct {
	guards   []binding.ShellApproval
	requests chan *vcsApproval
	timeout  time.Duration
	cancel   context.CancelFunc
	done     chan struct{}
}

// listenShellApprovals accepts one connection per command. The invoking host
// must permit connecting to the session-local approval socket.
func listenShellApprovals(ctx context.Context, path string, timeout time.Duration, guards []binding.ShellApproval) (*shellApprovalServer, error) {
	// Frontend paths can exceed Unix socket limits. The link stays in the
	// authenticated frontend snapshot; the actual socket has a short private path.
	directory, err := os.MkdirTemp("/tmp", "mekugi-vcs-")
	if err != nil {
		return nil, err
	}
	socket := filepath.Join(directory, "approval.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		os.Remove(directory)
		return nil, err
	}
	if err := os.Symlink(socket, path); err != nil {
		listener.Close()
		os.Remove(directory)
		return nil, err
	}
	cleanup := sync.OnceFunc(func() {
		listener.Close()
		os.Remove(path)
		os.Remove(directory)
	})
	ctx, cancel := context.WithCancel(ctx)
	server := &shellApprovalServer{guards: slices.Clone(guards), requests: make(chan *vcsApproval), timeout: timeout, cancel: cancel, done: make(chan struct{})}
	stop := context.AfterFunc(ctx, cleanup)
	go func() {
		var requests sync.WaitGroup
		defer func() {
			cancel()
			cleanup()
			stop()
			requests.Wait()
			close(server.done)
		}()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			requests.Go(func() { server.approve(ctx, conn) })
		}
	}()
	return server, nil
}

func (s *shellApprovalServer) close() {
	if s == nil {
		return
	}
	s.cancel()
	<-s.done
}

func (h *shellApprovalServer) approve(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	deadline := time.Now().Add(h.timeout)
	if err := conn.SetDeadline(deadline); err != nil {
		return
	}
	var message shellapproval.Message
	if json.UnmarshalDecode(jsontext.NewDecoder(io.LimitReader(conn, shellapproval.MaxMessage)), &message) != nil || len(message.Argv) == 0 {
		return
	}
	kind, known := binding.GuardForRequest(message.Kind)
	if !known || !slices.ContainsFunc(h.guards, func(g binding.ShellApproval) bool { return g.Kind == kind }) {
		json.MarshalWrite(conn, &shellapproval.Reply{Reason: "approval module is not active"})
		return
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		return
	}
	// A command killed while waiting closes its socket. There is no second
	// client message; any further read ends this request.
	gone := make(chan struct{})
	go func() {
		var b [1]byte
		conn.Read(b[:])
		close(gone)
	}()
	defer func() { conn.Close(); <-gone }()
	request := &vcsApproval{kind: message.Kind, thread: message.Thread, item: message.Item, cwd: message.Cwd, executable: message.Executable, argv: message.Argv, reply: make(chan shellapproval.Reply, 1), done: make(chan struct{})}
	defer close(request.done)
	request.outcome = "withdrawn"
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	answer := shellapproval.Reply{Reason: "no answer within 5 minutes"}
	select {
	case h.requests <- request:
		select {
		case answer = <-request.reply:
			request.outcome = ""
		case <-timer.C:
			request.outcome = "timed out"
		case <-gone:
			return
		case <-ctx.Done():
			return
		}
	case <-timer.C:
		request.outcome = "timed out"
	case <-gone:
		return
	case <-ctx.Done():
		return
	}
	// Permit the short denial write when the decision deadline just elapsed.
	if conn.SetWriteDeadline(time.Now().Add(time.Second)) == nil {
		json.MarshalWrite(conn, &answer)
	}
}
