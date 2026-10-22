package launch

import (
	"context"
	"fmt"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/creack/pty"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/libraries/diffrender"
)

func TestNativeExitRetainsFinalDiagnostics(t *testing.T) {
	for attempt := range 20 {
		t.Run(fmt.Sprint(attempt), func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.attached.cwd = t.TempDir()
			u.testMainFrame(80, 24, 0)
			release := make(chan struct{})
			launcher := &nativeReattachLauncher{run: func(ctx context.Context, start sessionapi.Start, api *sessionapi.Server) error {
				if err := api.SetSummary(sessionapi.Summary{Thread: start.Resume, Workspace: u.attached.cwd, Ready: true}); err != nil {
					return err
				}
				var request sessionapi.NativeRequest
				select {
				case request = <-api.Native().Requests():
				case <-ctx.Done():
					return ctx.Err()
				}
				frame := sessionapi.NativeFrame{View: u.attached.presentation.View()}
				if err := api.Native().Publish(request.Attachment, frame); err != nil {
					t.Error(err)
				}
				request.Reply <- ""
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
				frame.Facts.Diagnostics = "final host stderr"
				if err := api.Native().Publish(request.Attachment, frame); err != nil {
					t.Error(err)
				}
				return nil
			}}
			hub := sessionapi.NewHub(t.Context(), launcher)
			defer hub.Close()
			server := httptest.NewServer(sessionapi.Versioned(hub.Handler()))
			defer server.Close()
			rpc, err := sessionapi.OpenRPC(t.Context(), server.Client(), server.URL+sessionapi.RPCPath, "")
			if err != nil {
				t.Fatal(err)
			}
			defer rpc.Close()
			serverRPCCall(t, rpc, "session/start", "", sessionapi.Start{Workspace: u.attached.cwd, Model: "fixture", Resume: "main"})
			connection, err := sessionapi.OpenNative(t.Context(), server.Client(), server.URL, "main")
			close(release)
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			master, slave, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			defer slave.Close()
			if err := pty.Setsize(master, &pty.Winsize{Cols: 80, Rows: 24}); err != nil {
				t.Fatal(err)
			}
			readDone := make(chan struct{})
			go func() { _, _ = io.Copy(io.Discard, master); close(readDone) }()
			defer func() { slave.Close(); <-readDone }()
			terminal, _ := u.terminalViews.NewAttachment(diffrender.DarkTheme)
			client := nativeClient{connection: connection, renderer: nativeClientRenderer{terminal: terminal, views: u.terminalViews}}
			if _, err := client.run(t.Context(), slave, slave); err != nil {
				t.Fatal(err)
			}
			if got := client.event.Frame.Facts.Diagnostics; got != "final host stderr" {
				t.Fatalf("lost final diagnostics: %q", got)
			}
		})
	}
}
