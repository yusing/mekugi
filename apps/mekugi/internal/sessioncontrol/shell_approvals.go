package sessioncontrol

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

// ShellApprovalRequest is one guarded command waiting for the user. The guard
// handler owns its lifetime: it closes Done when the command stops waiting,
// after setting Outcome, so the UI never answers a request nobody reads.
// Source: apps/mekugi/internal/router/vcs_guard.go:20:167@[eaf0b69a] approval transport.
type ShellApprovalRequest struct {
	Thread, Cwd string
	Kind        string
	Item        string // Exact commandExecution item from the native hook.
	Executable  string
	Argv        []string
	Reply       chan shellapproval.Reply // Buffered; the UI's answer.
	Done        chan struct{}
	Outcome     string // Why the request ended without the UI's answer.
}

func (a *ShellApprovalRequest) Finished() bool {
	select {
	case <-a.Done:
		return true
	default:
		return false
	}
}

// ShellApprovals owns the shared VCS/sudo decision channel independently
// of command reports. Request completion and secret handling stay with callers.
type ShellApprovals struct {
	guards   []binding.ShellApproval
	requests chan *ShellApprovalRequest
	timeout  time.Duration
	cancel   context.CancelFunc
	done     chan struct{}
}

// ListenShellApprovals accepts one connection per command. The invoking host
// must permit connecting to the session-local approval socket.
func ListenShellApprovals(ctx context.Context, path string, timeout time.Duration, guards []binding.ShellApproval) (*ShellApprovals, error) {
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
	server := &ShellApprovals{guards: slices.Clone(guards), requests: make(chan *ShellApprovalRequest), timeout: timeout, cancel: cancel, done: make(chan struct{})}
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

// Requests supplies invocation-local decisions, independent of report delivery.
func (s *ShellApprovals) Requests() <-chan *ShellApprovalRequest {
	if s == nil {
		return nil
	}
	return s.requests
}

func (s *ShellApprovals) Close() {
	if s == nil {
		return
	}
	s.cancel()
	<-s.done
}

func (h *ShellApprovals) approve(ctx context.Context, conn net.Conn) {
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
	request := &ShellApprovalRequest{Kind: message.Kind, Thread: message.Thread, Item: message.Item, Cwd: message.Cwd, Executable: message.Executable, Argv: message.Argv, Reply: make(chan shellapproval.Reply, 1), Done: make(chan struct{})}
	defer close(request.Done)
	request.Outcome = "withdrawn"
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	answer := shellapproval.Reply{Reason: "no answer within 5 minutes"}
	select {
	case h.requests <- request:
		select {
		case answer = <-request.Reply:
			request.Outcome = ""
		case <-timer.C:
			request.Outcome = "timed out"
		case <-gone:
			return
		case <-ctx.Done():
			return
		}
	case <-timer.C:
		request.Outcome = "timed out"
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
