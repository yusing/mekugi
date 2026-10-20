package router

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/yusing/mekugi/internal/vcsguard"
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
	reply       chan vcsguard.Reply // Buffered; the UI's answer.
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

// listenVCSGuard accepts one connection per command. VCS approval does not
// support sandboxes that prevent connecting to the session-local socket.
func (h *execTrackHub) listenVCSGuard(ctx context.Context, path string) error {
	// Snapshot paths can exceed Unix socket address limits. Keep the socket in
	// a short private directory and link it from this session's snapshot.
	directory, err := os.MkdirTemp("/tmp", "mekugi-vcs-")
	if err != nil {
		return err
	}
	socket := filepath.Join(directory, "approval.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		os.Remove(directory)
		return err
	}
	if err := os.Symlink(socket, path); err != nil {
		listener.Close()
		os.Remove(directory)
		return err
	}
	cleanup := sync.OnceFunc(func() {
		listener.Close()
		os.Remove(path)
		os.Remove(directory)
	})
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		cleanup()
		return errors.New("command tracking stopped")
	}
	h.guardClose = cleanup
	h.mu.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(ctx, cleanup)
	h.wg.Go(func() {
		defer cleanup()
		defer cancel() // Closing the listener also withdraws accepted requests.
		defer stop()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			h.wg.Go(func() { h.approve(ctx, conn) })
		}
	})
	return nil
}

func (h *execTrackHub) approve(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	deadline := time.Now().Add(h.approvalTimeout)
	if err := conn.SetDeadline(deadline); err != nil {
		return
	}
	var message vcsguard.Message
	if json.UnmarshalDecode(jsontext.NewDecoder(io.LimitReader(conn, vcsguard.MaxMessage)), &message) != nil || len(message.Argv) == 0 {
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
	request := &vcsApproval{kind: message.Kind, thread: message.Thread, item: message.Item, cwd: message.Cwd, executable: message.Executable, argv: message.Argv, reply: make(chan vcsguard.Reply, 1), done: make(chan struct{})}
	defer close(request.done)
	request.outcome = "withdrawn"
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	answer := vcsguard.Reply{Reason: "no answer within 5 minutes"}
	select {
	case h.approvals <- request:
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
