package launch

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/diffrender"
)

func TestNativeUncapturedEditClickAcrossCompaction(t *testing.T) {
	for _, phase := range []string{"before", "across", "after"} {
		t.Run(phase, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			path := t.TempDir() + "/state.json"
			command := "cat > '" + path + "' <<'EOF'\n{\"checkpoint\":true}\nEOF"
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "work", "item": binding.ThreadItem{
				ID: "checkpoint", Type: "commandExecution", Command: command, Status: "completed", ExitCode: new(0),
			}})
			compact := func() {
				appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "compact", "item": binding.ThreadItem{ID: "compacted", Type: "contextCompaction"}})
			}
			if phase == "after" {
				compact()
			}
			n, view, _ := nativeDetailTestViews(t, u)
			terminal, _ := u.terminalViews.NewAttachment(diffrender.DarkTheme)
			t.Cleanup(func() { _ = terminal.Diff.Close() })
			terminal.Width, terminal.Height = 180, 30
			client := nativeClient{renderer: nativeClientRenderer{terminal: terminal, views: u.terminalViews}}
			publish := func() {
				client.accept(sessionapi.NativeEvent{Attachment: "first", Frame: sessionapi.NativeFrame{
					View: view.attached.presentation.View(), Main: view.attached.source.Publication(view.attached.presentation, nil),
				}})
			}
			publish()
			screen := vt.NewEmulator(180, 30)
			defer screen.Close()
			if _, err := client.renderer.render(t.Context(), screen, time.Now(), "test"); err != nil {
				t.Fatal(err)
			}
			x, y := 0, 0
			for row, text := range strings.Split(screen.String(), "\n") {
				if before, _, found := strings.Cut(text, "state.json"); found {
					x, y = ansi.StringWidth(before)+1, row+1
					break
				}
			}
			if x == 0 {
				t.Fatalf("Edit row missing:\n%s", screen.String())
			}
			client.calls = nil
			client.dispatch(terminalsession.Input{Kind: terminalsession.MouseInput, Text: fmt.Sprintf("\x1b[<0;%d;%dM", x, y)})
			if phase == "across" {
				compact()
				publish()
			}
			client.dispatch(terminalsession.Input{Kind: terminalsession.MouseInput, Text: fmt.Sprintf("\x1b[<0;%d;%dm", x, y)})
			if len(client.calls) != 1 || client.calls[0].operation.Details == nil {
				t.Fatalf("painted Edit did not request details: calls=%+v notice=%q", client.calls, client.notice)
			}
			call := client.calls[0]
			request := sessionapi.NativeRequest{Attachment: call.target.Attachment, Thread: call.target.Thread, Version: call.target.Version, Workspace: u.attached.cwd, Context: t.Context(), Operation: call.operation}
			admitted, refusal := n.admit(request, &u.attached, u.Thread)
			if refusal != "" {
				t.Fatal(refusal)
			}
			if refusal := admitted.applyView(request.Operation); refusal != "" {
				t.Fatal(refusal)
			}
			frame := nativeDetailFrame(view)
			if frame.Details == nil || len(frame.Details.View.Pages) == 0 {
				t.Fatal("Edit did not publish operation details")
			}
			page := frame.Details.View.Pages[0]
			if page.Code != command || !page.Exited || page.ExitCode != 0 || page.Lang == "diff" {
				t.Fatalf("details lost original command/outcome or invented a diff: %+v", page)
			}
			frame.Main = view.attached.source.Publication(view.attached.presentation, nil)
			client.accept(sessionapi.NativeEvent{Attachment: "first", Frame: frame})
			if _, err := client.renderer.render(t.Context(), screen, time.Now(), "test"); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(screen.String(), `"checkpoint":true`) {
				t.Fatalf("terminal did not deliver original edit source:\n%s", screen.String())
			}
			for _, wrong := range []sessionapi.NativeDetailRequest{
				{Kind: sessionapi.NativeEntryDetails, Source: call.operation.Details.Source, Path: path + ".foreign"},
				{Kind: sessionapi.NativeEntryDetails, Source: 999, Path: path},
			} {
				if refusal := view.openDetails(wrong); refusal == "" || nativeDetailFrame(view).Details.View.ID != frame.Details.View.ID {
					t.Fatal("invalid Edit target replaced the admitted detail selection")
				}
			}
		})
	}
}

func TestNativeUncapturedTrackedEditSelectsOriginalOperation(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	var segments []sessionpresentation.ActivitySegment
	for _, command := range []string{"cat > a.go", "cat > b.go"} {
		output := u.activity.Outputs(u.attached.session.Scope()).New()
		output.Finish(new(""), new(0))
		segments = append(segments, sessionpresentation.ActivitySegment{Source: command, Text: execSegmentText(command), Output: output})
	}
	command := "cat > a.go; cat > b.go"
	u.attached.source.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{Seq: 1, Agent: "Main", Kind: "tool", CallID: "cmd", Text: toolActivityShell(command), Native: &sessionpresentation.NativeItem{Thread: "main", Item: "cmd", Command: command, Segments: segments}}}})
	_, view, _ := nativeDetailTestViews(t, u)
	if refusal := view.openDetails(sessionapi.NativeDetailRequest{Kind: sessionapi.NativeEntryDetails, Source: 1, Path: "b.go"}); refusal != "" {
		t.Fatal(refusal)
	}
	frame := nativeDetailFrame(view)
	if frame.Details == nil {
		t.Fatal("tracked Edit did not publish details")
	}
	for _, page := range frame.Details.View.Pages {
		if page.ID == frame.Details.Selected && page.Code == segments[1].Source {
			return
		}
	}
	t.Fatalf("clicked b.go selected another operation: %+v", frame.Details)
}
