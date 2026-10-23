package launch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/modules/activity/feed"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
	"golang.org/x/term"
)

func TestNativeClientQueuedEscapeContinuation(t *testing.T) {
	for _, tc := range []struct {
		name, suffix string
		age          time.Duration
		want         []terminalsession.Input
	}{
		{"reports", "[O\x1b[<35;1;32M\x1b[<65;40;8M\x1b[I\x1b[<35;10;34M\x1b[<32;7;35M", time.Second, []terminalsession.Input{
			{Kind: terminalsession.FocusInput, Text: "\x1b[O"},
			{Kind: terminalsession.MouseInput, Text: "\x1b[<35;1;32M"},
			{Kind: terminalsession.MouseInput, Text: "\x1b[<65;40;8M"},
			{Kind: terminalsession.FocusInput, Text: "\x1b[I"},
			{Kind: terminalsession.MouseInput, Text: "\x1b[<35;10;34M"},
			{Kind: terminalsession.MouseInput, Text: "\x1b[<32;7;35M"},
		}},
		{"paste", "[200~x\x1b[201~", time.Second, []terminalsession.Input{{Kind: terminalsession.PasteStart}, {Kind: terminalsession.PasteInput, Text: "x"}, {Kind: terminalsession.PasteEnd}}},
		{"arrow", "[D", time.Second, []terminalsession.Input{{Kind: terminalsession.KeyInput, Text: "\x1b[D"}}},
		{"waiting", "", 39 * time.Millisecond, nil},
		{"standalone", "", 40 * time.Millisecond, []terminalsession.Input{{Kind: terminalsession.KeyInput, Text: "\x1b"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, _ := newAppServerTestUI()
			c := nativeTestPromptClient(u)
			r := nativeClientRuntime{active: c}
			now := time.Now()
			r.decode(27, now.Add(-tc.age))
			keys := make(chan byte, len(tc.suffix))
			for _, key := range []byte(tc.suffix) {
				keys <- key
			}
			r.drainKeys(keys, now)
			var got []terminalsession.Input
			for _, input := range c.input {
				got = append(got, input.event)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("decoded input = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestNativeClientInputDrainBoundAndEOF(t *testing.T) {
	u, _ := newAppServerTestUI()
	c := nativeTestPromptClient(u)
	r := nativeClientRuntime{active: c}
	keys := make(chan byte, 300)
	for range cap(keys) {
		keys <- 'x'
	}
	r.drainKeys(keys, time.Now())
	if len(keys) != 44 || len(c.input) != 256 {
		t.Fatalf("input drain did not yield: remaining=%d decoded=%d", len(keys), len(c.input))
	}
	close(keys)
	r.drainKeys(keys, time.Now())
	if len(c.input) != cap(keys) {
		t.Fatalf("closed input was not drained to EOF: decoded=%d", len(c.input))
	}
}

func TestNativeClientPTYDraftAndDetach(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.attached.cwd = t.TempDir()
	u.attached.presentation.Status.Text = "Ready"
	for i := range 60 {
		u.testActivity(u.shell.Transcript).Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{Seq: uint64(i + 1), Agent: "Main", Kind: "final", Text: fmt.Sprintf("completed-transcript-row-%02d", i), Observed: time.Now().Add(-time.Minute)}}})
	}
	u.testMainFrame(80, 24, 0)
	frame := sessionapi.NativeFrame{View: u.attached.presentation.View(), Main: u.attached.source.Publication(u.attached.presentation, nil), Agents: u.attached.childSource.Publication(u.attached.presentation, u.attached.source)}
	frame.Main.Entries = append([]activity.Entry{{Seq: 100, Agent: "Main", Kind: "tool", Text: "retained command", Outputs: map[int]int{1: 2}, Values: []activity.Value{{Kind: "op", Verb: "Run", Label: "printf retained", Source: 100, Output: 1, Exited: true, Tail: []string{"retained tail"}, TailOmitted: 1}}}}, frame.Main.Entries...)
	views := make(chan context.Context, 1)
	burst := make(chan struct{})
	outputAcknowledged := make(chan sessionapi.NativeRequest, 1)
	releaseOutput := make(chan struct{})
	references := make(chan sessionpresentation.ReferenceIntent, 1)
	var mutations atomic.Int32
	launcher := &nativeReattachLauncher{run: func(ctx context.Context, start sessionapi.Start, api *sessionapi.Server) error {
		if err := api.SetSummary(sessionapi.Summary{Thread: start.Resume, Workspace: u.attached.cwd, Ready: true}); err != nil {
			return err
		}
		var attachment string
		var pendingOutput *sessionapi.NativeOutputDemand
		effects := nativeViewEffects{}
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-releaseOutput:
				releaseOutput = nil
				for _, demand := range pendingOutput.Demands {
					frame.MainOutput = append(frame.MainOutput, activity.OutputResult{Demand: demand, View: feed.OutputView{Lines: []string{"retained prefix", "retained tail"}}})
				}
				if err := api.Native().Publish(attachment, frame); err != nil {
					t.Error(err)
				}
				frame.MainOutput = nil
			case <-burst:
				for i := range 80 {
					effects.deliver("main", []sessionpresentation.ActivityDelivery{{Command: &sessionpresentation.CommandDelivery{Kind: sessionpresentation.CommandDelta, Key: [3]string{"main", "turn", "cmd"}, Delta: fmt.Sprintf("line %d\n", i)}}})
				}
				effects.add(sessionapi.NativeEffect{Restore: &composer.Draft{Text: "burst survived"}})
				frame.Effects = effects.publication()
				frame.Activity = effects.activityPublication()
				if err := api.Native().Publish(attachment, frame); err != nil {
					t.Error(err)
				}
			case request := <-api.Native().Requests():
				if request.Attach {
					attachment = request.Attachment
					views <- request.ViewContext
				} else if receipt := request.Operation.Receipt; receipt != nil {
					effects.acknowledge(receipt.Effect, false)
				} else if output := request.Operation.Output; output != nil {
					pendingOutput = output
					// Acknowledge the unchanged presentation before output arrives.
					request.Reply <- ""
					outputAcknowledged <- request
					continue
				} else if reference := request.Operation.Reference; reference != nil {
					references <- *reference
					effects.add(sessionapi.NativeEffect{Thread: "main", Target: new(reference.Target), Selection: &sessionapi.NativeSelection{Description: reference.Description, Text: reference.Text, Source: reference.Source}})
				} else if request.Operation.Receipt == nil && request.Operation.Completion == nil && request.Operation.Preferences == nil && request.Operation.Focused == nil {
					mutations.Add(1)
				}
				frame.Effects = effects.publication()
				frame.Activity = effects.activityPublication()
				if err := api.Native().Publish(request.Attachment, frame); err != nil {
					request.Reply <- err.Error()
				} else {
					request.Reply <- ""
				}
			}
		}
	}}
	hub := sessionapi.NewHub(t.Context(), launcher)
	defer hub.Close()
	dir, err := os.MkdirTemp("", "mekugi-native-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	listener, err := sessionapi.ListenControl(filepath.Join(dir, "control.sock"))
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: sessionapi.Versioned(hub.Handler())}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Close()
		if err := <-served; !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("serve: %v", err)
		}
	})
	transport := sessionapi.ControlTransport(filepath.Join(dir, "control.sock"))
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	rpc, err := sessionapi.OpenRPC(t.Context(), client, "http://mekugi"+sessionapi.RPCPath, "")
	if err != nil {
		t.Fatal(err)
	}
	defer rpc.Close()
	serverRPCCall(t, rpc, "session/start", "", sessionapi.Start{Workspace: u.attached.cwd, Model: "fixture", Resume: "main"})
	connection, err := sessionapi.OpenNative(t.Context(), &http.Client{Transport: transport}, "http://mekugi", "main")
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	viewContext := <-views
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	if err := pty.Setsize(master, &pty.Winsize{Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	before, err := term.GetState(int(slave.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	frames := make(chan []byte, 64)
	go func() {
		defer close(frames)
		var pending synchronizedFrameBuffer
		buffer := make([]byte, 8192)
		for {
			n, err := master.Read(buffer)
			pending.Append(buffer[:n])
			for {
				frame, ok := pending.Next()
				if !ok {
					break
				}
				select {
				case frames <- frame:
				case <-t.Context().Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	terminal, _ := u.terminalViews.NewAttachment(diffrender.DarkTheme)
	terminal.Transcript.Expansion = 2
	c := nativeClient{connection: connection, renderer: nativeClientRenderer{terminal: terminal, views: u.terminalViews}}
	done := make(chan error, 1)
	go func() { _, err := c.run(t.Context(), slave, slave); done <- err }()
	screen := vt.NewEmulator(80, 24)
	// The actual startup queries terminal colors. Consume the emulator's replies
	// and return them through the PTY, just as a physical terminal does.
	repliesDone := make(chan struct{})
	go func() { defer close(repliesDone); _, _ = io.Copy(master, screen) }()
	defer func() {
		// Close the reply pipe and join its reader before Emulator.Close changes
		// the emulator's non-concurrent lifecycle flag.
		_ = screen.InputPipe().(io.Closer).Close()
		<-repliesDone
		_ = screen.Close()
	}()
	await := func(text string) {
		t.Helper()
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		for {
			select {
			case frame, ok := <-frames:
				if !ok {
					t.Fatal("terminal reader closed")
				}
				if _, err := screen.Write(frame); err != nil {
					t.Fatal(err)
				}
				if strings.Contains(screen.String(), text) {
					return
				}
			case err := <-done:
				t.Fatalf("client exited early: %v", err)
			case <-timer.C:
				t.Fatalf("terminal did not display %q:\n%s", text, screen.String())
			}
		}
	}
	await("completed-transcript-row-59")
	burst <- struct{}{}
	await("burst survived")
	if _, err := master.WriteString("\x03"); err != nil {
		t.Fatal(err)
	}
	// Settle startup replies before testing navigation on an idle publication.
	time.Sleep(500 * time.Millisecond)
	for draining := true; draining; {
		select {
		case frame := <-frames:
			_, _ = screen.Write(frame)
		default:
			draining = false
		}
	}
	for _, step := range []struct{ keys, visible string }{
		{"\x02\x1b[5~", "completed-transcript-row-51"},
		{"\x02\x1b[6~", "completed-transcript-row-57"},
		{"\x1b[<64;40;8M", "completed-transcript-row-53"},
	} {
		if _, err := master.WriteString(step.keys); err != nil {
			t.Fatal(err)
		}
		await(step.visible)
	}
	if _, err := master.WriteString("draft stays local"); err != nil {
		t.Fatal(err)
	}
	await("draft stays local")
	var x, y int
	var selected string
	for row, line := range strings.Split(screen.String(), "\n") {
		if column := strings.Index(line, "completed-transcript-row-"); column >= 0 {
			x, y, selected = ansi.StringWidth(line[:column])+1, row+1, line[column:column+len("completed-transcript-row-00")]
			break
		}
	}
	if selected == "" {
		t.Fatal("fixture has no visible transcript selection target")
	}
	select {
	case <-outputAcknowledged:
		t.Fatal("offscreen output was acquired before selection capture")
	default:
	}
	if _, err := fmt.Fprintf(master, "\x1b[<0;%d;%dM", x, y); err != nil {
		t.Fatal(err)
	}
	select {
	case request := <-outputAcknowledged:
		if request.Version != frame.View.Version || request.Operation.Output.Agent || len(request.Operation.Output.Demands) != 1 {
			t.Fatalf("selection acquisition lost its unchanged presentation: %+v", request)
		}
		demand := request.Operation.Output.Demands[0]
		if demand.Source != 100 || demand.Output != 1 || demand.Version != 2 {
			t.Fatalf("selection acquisition changed source identity: %+v", demand)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("full-document selection did not acquire offscreen output")
	}
	if _, err := fmt.Fprintf(master, "\x1b[<32;%d;%dM\x1b[<0;%d;%dmr", x+len(selected), y, x+len(selected), y); err != nil {
		t.Fatal(err)
	}
	// Keep hydration unavailable through several real client loop iterations,
	// allowing the early acknowledgement and buffered gesture to settle first.
	time.Sleep(100 * time.Millisecond)
	close(releaseOutput)
	select {
	case reference := <-references:
		if reference.Text != selected || reference.Displayed != frame.View.Displayed || !reference.Version.SameAttachment(frame.View.Version) {
			t.Fatalf("hydration lost the frozen reference: %+v; want %q at (%d,%d); row=%q", reference, selected, x, y, strings.Split(screen.String(), "\n")[y-1])
		}
		await(composer.SelectionPrefix + reference.Description + "]")
	case <-time.After(5 * time.Second):
		t.Fatal("buffered drag/release/reference did not survive delayed hydration")
	}
	if _, err := master.WriteString("continued editing"); err != nil {
		t.Fatal(err)
	}
	await("continued editing")
	if _, err := master.WriteString("\x03/quit\r"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("client did not detach")
	}
	select {
	case <-viewContext.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("detach retained native view")
	}
	if mutations.Load() != 0 {
		t.Fatal("local editing or detach issued a semantic execution request")
	}
	if strings.Contains(c.notice, "could not be loaded") {
		t.Fatalf("selection hydration failed: %s", c.notice)
	}
	after, err := term.GetState(int(slave.Fd()))
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("terminal modes were not restored: %v", err)
	}
}
