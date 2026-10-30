package sessionapi

import (
	"context"
	"errors"
	"testing"
)

type nativeStartupLauncher struct {
	managedLauncher
	run func(context.Context, Start, *Server) error
}

func (l *nativeStartupLauncher) Run(ctx context.Context, start Start, server *Server) error {
	return l.run(ctx, start, server)
}

func TestNativeStartupReportsInitializationFailure(t *testing.T) {
	t.Parallel()
	launcher := &nativeStartupLauncher{run: func(context.Context, Start, *Server) error {
		return &NativeExit{Code: 2, Message: "The selected invocation option is unavailable."}
	}}
	hub := NewHub(t.Context(), launcher)
	defer hub.Close()
	client, _ := serveControl(t, hub.Handler())
	connection, err := StartNative(t.Context(), client, "http://mekugi", NativeStart{Workspace: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	_, err = connection.Next()
	outcome, ok := errors.AsType[*NativeExit](err)
	if !ok || outcome.Code != 2 || outcome.Message != "The selected invocation option is unavailable." {
		t.Fatalf("startup outcome disappeared before attachment: %v", err)
	}
}

func TestNativeStartupDisconnectAndJoinedOutcome(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	finish, cleanup, joined := make(chan struct{}), make(chan struct{}), make(chan struct{})
	running := make(chan context.Context, 1)
	launcher := &nativeStartupLauncher{run: func(ctx context.Context, start Start, server *Server) error {
		running <- ctx
		if start.Native == nil || start.Native.Workspace != workspace {
			return errors.New("native startup arguments were lost")
		}
		for {
			select {
			case request := <-server.Native().Requests():
				if !request.Attach || request.Thread != "" || request.Startup == "" || request.Workspace != workspace {
					return errors.New("native startup attachment was retargeted")
				}
				frame := nativeTestFrame("", 1)
				frame.View.Info.Workspace = workspace
				if err := server.Native().Publish(request.Attachment, frame); err != nil {
					return err
				}
				request.Reply <- ""
			case <-finish:
				close(cleanup)
				<-joined
				return &NativeExit{Code: 17, Message: "Host exited during work."}
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}}
	hub := NewHub(t.Context(), launcher)
	defer hub.Close()
	client, _ := serveControl(t, hub.Handler())
	connection, err := StartNative(t.Context(), client, "http://mekugi", NativeStart{Workspace: workspace})
	if err != nil {
		t.Fatal(err)
	}
	event, err := connection.Next()
	if err != nil || event.Startup == "" || event.Target().Thread != "" {
		t.Fatalf("startup snapshot: %+v, %v", event, err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	if ctx := <-running; ctx.Err() != nil {
		t.Fatalf("client disconnect canceled runtime: %v", ctx.Err())
	}
	connection, err = openNativeSubscription(t.Context(), client, "http://mekugi", "view/attach", RPCParams{Startup: event.Startup})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.Next(); err != nil {
		t.Fatal(err)
	}
	close(finish)
	<-cleanup
	result := make(chan error, 1)
	go func() { _, err := connection.Next(); result <- err }()
	select {
	case err := <-result:
		t.Fatalf("runtime outcome preceded joined cleanup: %v", err)
	default:
	}
	close(joined)
	outcome, ok := errors.AsType[*NativeExit](<-result)
	if !ok || outcome.Code != 17 || outcome.Message != "Host exited during work." {
		t.Fatalf("joined runtime outcome: %+v", outcome)
	}
}

func TestRunNativeLiteralInputAndFamilyJoin(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	prompt := "  /literal\n!still literal\n"
	childJoined := make(chan struct{})
	var hub *Hub
	launcher := &nativeStartupLauncher{run: func(ctx context.Context, start Start, server *Server) error {
		if start.Native == nil || start.Native.Workspace != workspace {
			return errors.New("lost native admission")
		}
		if err := server.SetSummary(Summary{Thread: "root", Workspace: workspace, Ready: true}); err != nil {
			return err
		}
		select {
		case intent := <-server.Intents():
			if intent.Action != Send || intent.Thread != "root" || intent.Text != "" || intent.Prepared.Draft.Text != prompt {
				return errors.New("literal prompt was changed")
			}
			if !intent.Prepared.Admit(ctx, nil) {
				return errors.New("prepared input was not admitted")
			}
			_, err := hub.StartPrepared(ctx, "root", "child", workspace, func(ctx context.Context, child *Server) error {
				if err := child.SetSummary(Summary{Thread: "child", Workspace: workspace, Ready: true}); err != nil {
					return err
				}
				<-ctx.Done()
				close(childJoined)
				return ctx.Err()
			})
			if err != nil {
				return err
			}
			intent.Reply <- ""
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	hub = NewHub(t.Context(), launcher)
	defer hub.Close()
	id, err := hub.RunNative(t.Context(), NativeStart{Workspace: workspace}, prompt)
	if err != nil || id != "root" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	select {
	case <-childJoined:
	default:
		t.Fatal("returned before child cleanup")
	}
}

func TestRunNativeCancellationJoinsRuntime(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	joined := make(chan struct{})
	launcher := &nativeStartupLauncher{run: func(ctx context.Context, _ Start, server *Server) error {
		cancel()
		<-ctx.Done()
		close(joined)
		return ctx.Err()
	}}
	hub := NewHub(t.Context(), launcher)
	defer hub.Close()
	_, err := hub.RunNative(ctx, NativeStart{Workspace: t.TempDir()}, "literal")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	select {
	case <-joined:
	default:
		t.Fatal("runtime cleanup not joined")
	}
}
