package launch

import (
	"context"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/libraries/diffrender"
)

// Only host responses are synthetic. The physical client reads PTY dimensions,
// paints the chooser, and submits its demand over the native stream protocol to
// the ordinary Resume admission and host-list owner. No server scratch renderer
// or navigation key can request the initial/resize pages for this fixture.
func TestUISnapshotNativeResumePagingPTY(t *testing.T) {
	u, w := newResumeSessionTestUI(t)
	u.Startup.Workspace = "/work"
	u.clock = func() time.Time { return time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC) }
	for i := range 50 {
		resumeTestMembers(t, u, fmt.Sprintf("saved-%02d", i))
	}
	if err := openResumePicker(&u.attached, &u.Client, &u.picker, u.clock, &u.resumePicker, true); err != nil {
		t.Fatal(err)
	}
	first := resumeTestOne(t, w, "thread/list")
	responses := make(chan appserver.Message)
	requests := make(chan resumeTestRequest, 64)
	heldPages := make(chan sessionpresentation.ResumeAction, 1)
	release := make(chan struct{})
	replace := make(chan struct{})
	var hold atomic.Bool
	var pageActions atomic.Int32
	hold.Store(true)
	launcher := &nativeReattachLauncher{run: func(ctx context.Context, start sessionapi.Start, api *sessionapi.Server) error {
		if err := api.SetSummary(sessionapi.Summary{Thread: "main", Workspace: "/work", Ready: true}); err != nil {
			return err
		}
		var attachment string
		var held *sessionapi.NativeRequest
		apply := func(action sessionpresentation.ResumeAction) error {
			return u.nativeSession.applyResumeAction(&u.attached, &u.picker, &u.Editor, u.clock, &u.side, &u.statusPanel, &u.resumePicker, &u.quitRequested, action)
		}
		for {
			var reply chan string
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-replace:
				if err := openResumePicker(&u.attached, &u.Client, &u.picker, u.clock, &u.resumePicker, false); err != nil {
					return err
				}
			case <-release:
				if held == nil {
					return fmt.Errorf("no held page demand")
				}
				if err := apply(*held.Operation.Resume); err != nil {
					return err
				}
				reply, held = held.Reply, nil
			case response := <-responses:
				if err := u.nativeSession.dispatchHostMessage(&u.attached, &u.picker, &u.Editor, u.clock, &u.side, &u.statusPanel, &u.resumePicker, ctx, response); err != nil {
					return err
				}
			case request := <-api.Native().Requests():
				if request.Attach {
					attachment = request.Attachment
				}
				if action := request.Operation.Resume; action != nil {
					if action.Kind == sessionpresentation.ResumePage {
						pageActions.Add(1)
						if hold.Swap(false) {
							held = &request
							heldPages <- *action
							continue
						}
					}
					if err := apply(*action); err != nil {
						return err
					}
				}
				reply = request.Reply
			}
			settlePresentation(&u.attached, &u.Client, &u.picker, &u.Editor, u.settings, u.serviceTiers, u.title, u.backendVersion, false, u.side, u.clock, &u.questions, &u.approvals, true)
			frame := sessionapi.NativeFrame{View: u.attached.presentation.View(), Main: u.attached.source.Publication(u.attached.presentation, nil)}
			if err := api.Native().Publish(attachment, frame); err != nil {
				return err
			}
			if reply != nil {
				reply <- ""
			}
			for _, request := range resumeTestRequests(t, w) {
				requests <- request
			}
		}
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
	serverRPCCall(t, rpc, "session/start", "", sessionapi.Start{Workspace: t.TempDir(), Model: "fixture", Resume: "main"})
	connection, err := sessionapi.OpenNative(t.Context(), server.Client(), server.URL, "main")
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
	screen := vt.NewEmulator(80, 18)
	repliesDone := make(chan struct{})
	go func() { defer close(repliesDone); _, _ = io.Copy(master, screen) }()
	defer func() {
		_ = screen.InputPipe().(io.Closer).Close()
		<-repliesDone
		_ = screen.Close()
	}()
	resize := func(height int) {
		t.Helper()
		screen.Resize(80, height)
		if err := pty.Setsize(master, &pty.Winsize{Cols: 80, Rows: uint16(height)}); err != nil {
			t.Fatal(err)
		}
	}
	resize(18)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	chunks := readPTYChunks(ctx, master, 8192, 64)
	terminal, _ := u.terminalViews.NewAttachment(diffrender.DarkTheme)
	c := nativeClient{connection: connection, renderer: nativeClientRenderer{terminal: terminal, views: u.terminalViews}}
	done := make(chan error, 1)
	go func() { _, err := c.run(ctx, slave, slave); done <- err }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("native client did not stop")
		}
	}()
	var pending synchronizedFrameBuffer
	await := func(text string) {
		t.Helper()
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		for {
			select {
			case chunk, ok := <-chunks:
				if !ok {
					t.Fatal("terminal reader closed")
				}
				pending.Append(chunk)
				for {
					frame, ok := pending.Next()
					if !ok {
						break
					}
					_, _ = screen.Write(frame)
					if strings.Contains(screen.String(), text) {
						return
					}
				}
			case <-timer.C:
				t.Fatalf("terminal did not show %q:\n%s", text, screen.String())
			}
		}
	}
	next := func(cursor string) resumeTestRequest {
		t.Helper()
		select {
		case request := <-requests:
			if request.Method != "thread/list" || request.Params.Cursor != cursor {
				t.Fatalf("host request = %+v, want cursor %q", request, cursor)
			}
			return request
		case <-time.After(5 * time.Second):
			t.Fatalf("no host page request for %q", cursor)
			return resumeTestRequest{}
		}
	}
	quiet := func(actions int32) {
		t.Helper()
		select {
		case request := <-requests:
			t.Fatalf("unexpected page request: %+v", request)
		case <-time.After(150 * time.Millisecond):
		}
		if got := pageActions.Load(); got != actions {
			t.Fatalf("page actions = %d, want %d", got, actions)
		}
	}
	awaitHeld := func(after string) {
		t.Helper()
		select {
		case action := <-heldPages:
			if action.After != after {
				t.Fatalf("page target = %q, want %q", action.After, after)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("native client did not request the underfilled page after %q:\n%s", after, screen.String())
		}
	}
	page := func(request resumeTestRequest, begin, end int, cursor string) {
		t.Helper()
		var rows []string
		for i := range max(1, 25-(end-begin)) {
			rows = append(rows, fmt.Sprintf(`{"id":"stock-%02d","preview":"EXCLUDED STOCK SESSION"}`, i))
		}
		for i := begin; i < end; i++ {
			rows = append(rows, fmt.Sprintf(`{"id":"saved-%02d","preview":"Saved conversation %02d","cwd":"/work","gitInfo":{"branch":"main"}}`, i, i))
		}
		responses <- appserver.Message{ID: []byte(fmt.Sprint(request.ID)), Result: []byte(fmt.Sprintf(`{"data":[%s],"nextCursor":%q}`, strings.Join(rows, ","), cursor))}
	}
	send := func(text string) {
		t.Helper()
		if _, err := master.WriteString(text); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := func(name string) {
		t.Helper()
		assertNativeUISnapshot(t, name, strings.Split(screen.String(), "\n"))
	}
	await("Loading sessions")
	page(first, 0, 2, "fill")
	await("Saved conversation 01")
	snapshot("native-resume-underfilled")
	awaitHeld("saved-01")
	resize(20)
	await("Saved conversation 01")
	quiet(1) // No repeated demand before the first operation is admitted.
	release <- struct{}{}
	fill := next("fill")
	await("loading more")
	resize(18)
	await("loading more")
	quiet(1) // Loading publication suppresses requests after acknowledgement too.
	page(fill, 2, 22, "resize")
	await("Saved conversation 07")
	snapshot("native-resume-filled")
	quiet(1)
	resize(40)
	resized := next("resize")
	await("Saved conversation 21")
	page(resized, 22, 40, "failure")
	await("Saved conversation 29")
	snapshot("native-resume-resized")
	quiet(2)
	resize(55)
	failed := next("failure")
	responses <- appserver.Message{ID: []byte(fmt.Sprint(failed.ID)), Error: &appserver.Error{Code: -1, Message: "store offline"}}
	await("Could not list sessions: store offline")
	resize(56)
	await("store offline")
	quiet(3)
	resize(18)
	send("\t")
	restarted := next("")
	page(restarted, 0, 25, "search")
	await("Saved conversation 07")
	send("z")
	search := next("search")
	await("Searching")
	page(search, 25, 26, "scan") // No matching row: server scanning stays independent.
	scanned := next("scan")
	page(scanned, 26, 27, "")
	await("No results for your search")
	quiet(3)
	send("\x1b")
	await("Saved conversation 07")
	send("\t")
	restarted = next("")
	page(restarted, 0, 25, "stale")
	await("Saved conversation 07")
	hold.Store(true)
	resize(40)
	awaitHeld("saved-24")
	replace <- struct{}{}
	replacement := next("")
	await("Loading sessions")
	release <- struct{}{} // The captured old chooser must not page its replacement.
	quiet(4)
	page(replacement, 0, 1, "")
	await("Saved conversation 00")
	quiet(4)
}
