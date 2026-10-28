package sessionapi

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	threadcontrol "github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestNativeColdAttachmentUsesAdmittedAncestor(t *testing.T) {
	t.Parallel()
	for _, switched := range []bool{false, true} {
		name := "root_owner"
		if switched {
			name = "root_replaced"
		}
		t.Run(name, func(t *testing.T) { testNativeColdAttachment(t, switched) })
	}
}

func TestNonAttachableRefusesColdAndRegisteredViews(t *testing.T) {
	for _, live := range []bool{false, true} {
		for _, native := range []bool{false, true} {
			hub := NewHub(t.Context(), nil)
			root, child := NewLive(), NewLive()
			root.frame.Store(&Frame{Session: Session{Thread: "root"}, FamilyRoot: "root", Members: []threadcontrol.Participant{
				{Thread: "child", Parent: "root", Task: "/root/worker", NonAttachable: true},
			}})
			registerTestSession(hub, "root", root, Summary{Thread: "root", Workspace: "/workspace"})
			if live {
				registerTestSession(hub, "child", child, Summary{Thread: "child", Workspace: "/workspace"})
			}
			if failure := hub.attachThread(t.Context(), "child", localNativeView); failure == nil || failure.Data.Status != 403 {
				t.Fatalf("runtime attach accepted nonattachable child: %+v", failure)
			}
			conn := rawControl(t, hub.Handler(), native)
			controlInitialize(t, conn)
			controlWrite(t, conn, `{"jsonrpc":"2.0","id":"attach","method":"view/attach","params":{"thread":"child","subscription":"view"}}`)
			if response := controlRead(t, conn); response.Error == nil || response.Error.Data.Status != 403 {
				t.Fatalf("live=%v native=%v view attach: %+v", live, native, response)
			}
			select {
			case <-child.Native().Requests():
				t.Fatal("refused attachment reached the runtime")
			default:
			}
		}
	}
}

func testNativeColdAttachment(t *testing.T, switched bool) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	workspace := t.TempDir()
	members := []threadcontrol.Participant{
		{Thread: "root", Task: "/root", Status: threadcontrol.Working},
		{Thread: "branch", Parent: "root", Task: "/root/branch", Status: threadcontrol.Working},
		{Thread: "sibling", Parent: "root", Task: "/root/sibling", Status: threadcontrol.Working},
		{Thread: "nested", Parent: "branch", Task: "/root/branch/nested", Status: threadcontrol.Completed},
	}
	rootServer := make(chan *Server, 1)
	serve := func(ctx context.Context, server *Server, thread, caller string) error {
		if err := server.Publish(Frame{Session: Session{Thread: thread, Caller: caller, Workspace: workspace}, FamilyRoot: "root", Members: members, Agents: familyActivity("history-only", "retained history")}); err != nil {
			return err
		}
		if err := server.SetSummary(Summary{Thread: thread, Workspace: workspace, Ready: true}); err != nil {
			return err
		}
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case request := <-server.Native().Requests():
				if !request.Attach || request.Thread != thread {
					request.Reply <- "unexpected attachment target"
					continue
				}
				frame := nativeTestFrame(thread, 1)
				frame.View.Info.Workspace = workspace
				if err := server.Native().Publish(request.Attachment, frame); err != nil {
					return err
				}
				request.Reply <- ""
			}
		}
	}
	var hub *Hub
	var preparations, arbitraryCallbacks atomic.Int32
	expectedOwner := "root"
	if switched {
		expectedOwner = "branch"
	}
	preparing, release := make(chan struct{}), make(chan struct{})
	bindOwner := func(server *Server, thread string) {
		server.BindChildAttachment(func(ctx context.Context, owner, target string) error {
			if thread != expectedOwner {
				arbitraryCallbacks.Add(1)
				return errors.New("nonancestor cannot prepare this target")
			}
			if preparations.Add(1) == 1 {
				close(preparing)
			}
			if owner != expectedOwner || target != "nested" {
				return errors.New("attachment lost authoritative ancestor identity")
			}
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			_, err := hub.StartPrepared(ctx, owner, target, workspace, func(ctx context.Context, child *Server) error {
				return serve(ctx, child, target, "/root/branch/nested")
			})
			return err
		})
	}
	launcher := &nativeStartupLauncher{run: func(ctx context.Context, _ Start, server *Server) error {
		rootServer <- server
		bindOwner(server, "root")
		return serve(ctx, server, "root", "/root")
	}}
	hub = NewHub(ctx, launcher)
	defer hub.Close()
	if _, failure := hub.startSession(ctx, testJSON(t, Start{Workspace: workspace, Model: "codex"})); failure != nil {
		t.Fatal(failure)
	}
	if _, err := hub.StartPrepared(ctx, "root", "branch", workspace, func(ctx context.Context, server *Server) error {
		bindOwner(server, "branch")
		return serve(ctx, server, "branch", "/root/branch")
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := hub.StartPrepared(ctx, "root", "sibling", workspace, func(ctx context.Context, server *Server) error {
		server.BindChildAttachment(func(context.Context, string, string) error {
			arbitraryCallbacks.Add(1)
			return errors.New("sibling cannot prepare this target")
		})
		return serve(ctx, server, "sibling", "/root/sibling")
	}); err != nil {
		t.Fatal(err)
	}
	if switched {
		server := <-rootServer
		changes := make(chan []byte, 1)
		hub.mu.Lock()
		hub.viewers[changes] = struct{}{}
		hub.mu.Unlock()
		defer func() { hub.mu.Lock(); delete(hub.viewers, changes); hub.mu.Unlock() }()
		if err := server.Publish(Frame{Session: Session{Thread: "replacement", Caller: "/replacement", Workspace: workspace}, FamilyRoot: "replacement", Members: []threadcontrol.Participant{{Thread: "replacement", Task: "/replacement"}}}); err != nil {
			t.Fatal(err)
		}
		if err := server.SetSummary(Summary{Thread: "replacement", Workspace: workspace, Ready: true}); err != nil {
			t.Fatal(err)
		}
		for !hub.activeThread("replacement") {
			select {
			case <-changes:
			case <-ctx.Done():
				t.Fatal("root runtime did not replace its logical family")
			}
		}
	}
	client, _ := serveControl(t, hub.Handler())
	source, err := OpenNative(ctx, client, "http://mekugi", "sibling")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if _, err := source.Next(); err != nil {
		t.Fatal(err)
	}
	active, err := source.Attach(ctx, "branch")
	if err != nil {
		t.Fatal(err)
	}
	active.Close()
	for _, target := range []string{"unknown", "history-only"} {
		connection, err := source.Attach(ctx, target)
		if connection != nil {
			connection.Close()
		}
		if failure, ok := errors.AsType[*NativeFailure](err); !ok || failure.Status != 404 {
			t.Fatalf("unadmitted target %q: %v", target, err)
		}
	}
	if preparations.Load() != 0 || arbitraryCallbacks.Load() != 0 {
		t.Fatal("active or unadmitted target invoked a preparation callback")
	}

	type result struct {
		connection *NativeConnection
		err        error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			connection, err := source.Attach(ctx, "nested")
			results <- result{connection, err}
		}()
	}
	select {
	case <-preparing:
	case <-ctx.Done():
		t.Fatal("cold nested attachment did not reach the ancestor owner")
	}
	close(release)
	for range 2 {
		opened := <-results
		if opened.err != nil {
			t.Fatal(opened.err)
		}
		event, err := opened.connection.Next()
		opened.connection.Close()
		if err != nil || event.Target().Thread != "nested" {
			t.Fatalf("cold target publication: %+v, %v", event, err)
		}
	}
	if preparations.Load() != 1 || arbitraryCallbacks.Load() != 0 {
		t.Fatalf("concurrent cold attachment callbacks: ancestor=%d, sibling=%d", preparations.Load(), arbitraryCallbacks.Load())
	}
}
