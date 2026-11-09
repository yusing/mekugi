package launch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/contracts/activitydetail"
	"github.com/yusing/mekugi/libraries/diffrender"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	changehistory "github.com/yusing/mekugi/modules/changehistory/presentation"
	conversation "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

func nativeDetailTestViews(t *testing.T, u *appServerUI) (*nativeViews, *nativeSessionView, *nativeSessionView) {
	t.Helper()
	n := newNativeViews(t.Context(), sessionapi.NewLive().Native(), u.presentationState, u.terminalViews, u.activity, u.Requests)
	t.Cleanup(n.close)
	for _, id := range []string{"first", "second"} {
		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)
		if refusal := n.attach(sessionapi.NativeRequest{Attach: true, Attachment: id, Thread: u.Thread, Workspace: u.attached.cwd, Context: ctx, ViewContext: ctx}, &u.attached, u.Thread); refusal != "" {
			t.Fatal(refusal)
		}
		v := n.views[id]
		publishSessionView(&v.attached, &u.Client, &v.picker, "", preferences.Applied{}, u.serviceTiers, "", "", false, nil, true)
	}
	return n, n.views["first"], n.views["second"]
}

func nativeDetailFrame(v *nativeSessionView) sessionapi.NativeFrame {
	frame := sessionapi.NativeFrame{View: v.attached.presentation.View()}
	v.publishDetails(&frame)
	return frame
}

func TestNativeDetailsSelectAndCloseIndependently(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", "/workspace")
	u.Turn = "turn"
	firstOutput := u.activity.Outputs(u.attached.session.Scope()).New()
	secondOutput := u.activity.Outputs(u.attached.session.Scope()).New()
	firstOutput.Finish(new("first output\n"), new(0))
	secondOutput.Finish(new("second output\n"), new(0))
	seq := u.attached.source.LastSeq + 1
	u.attached.source.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{
		{Seq: seq, Agent: "Main", Kind: "tool", Text: "Run `first`", Native: &sessionpresentation.NativeItem{Thread: "main", Turn: "turn", Item: "first", Output: firstOutput}},
		{Seq: seq + 1, Agent: "Main", Kind: "tool", Text: "Run `second`", Native: &sessionpresentation.NativeItem{Thread: "main", Turn: "turn", Item: "second", Output: secondOutput}},
	}})
	n, first, second := nativeDetailTestViews(t, u)
	for i, view := range []*nativeSessionView{first, second} {
		before := view.attached.presentation.View()
		client := nativeClient{event: sessionapi.NativeEvent{Attachment: view.attached.view, Frame: sessionapi.NativeFrame{View: before}}}
		client.details(before.Version, sessionapi.NativeDetailRequest{Kind: sessionapi.NativeEntryDetails, Source: seq + uint64(i)})
		call := client.calls[0]
		request := sessionapi.NativeRequest{Attachment: call.target.Attachment, Thread: call.target.Thread, Version: call.target.Version, Workspace: u.attached.cwd, Context: t.Context(), Operation: call.operation}
		view.attached.feedback.Set("running progress", false, time.Now())
		after := publishSessionView(&view.attached, &u.Client, &view.picker, "", preferences.Applied{}, u.serviceTiers, "", "", false, nil, true)
		if after.Version == before.Version {
			t.Fatal("fixture did not advance the running publication")
		}
		admitted, refusal := n.admit(request, &u.attached, u.Thread)
		if refusal != "" {
			t.Fatal(refusal)
		}
		if refusal := admitted.applyView(request.Operation); refusal != "" {
			t.Fatal(refusal)
		}
		request.Workspace = "/replacement"
		if _, refusal := n.admit(request, &u.attached, u.Thread); refusal == "" {
			t.Fatal("detail request crossed workspace identity")
		}
		request.Workspace = u.attached.cwd
		request.Version.Attachment++
		if _, refusal := n.admit(request, &u.attached, u.Thread); refusal == "" {
			t.Fatal("detail request crossed attachment identity")
		}
		request.Version = before.Version
		model := view.attached.presentation
		if err := model.Display(sessionpresentation.DisplayIntent{Version: model.View().Version, Thread: sessionpresentation.Thread{ID: "child", Caller: "/root/child"}}); err != nil {
			t.Fatal(err)
		}
		if _, refusal := n.admit(request, &u.attached, u.Thread); refusal != "" {
			t.Fatalf("navigation invalidated the original detail source: %s", refusal)
		}
		if refusal := view.applyView(request.Operation); refusal != "" {
			t.Fatal(refusal)
		}
		if err := model.Display(sessionpresentation.DisplayIntent{Version: model.View().Version, Thread: before.Displayed}); err != nil {
			t.Fatal(err)
		}
	}
	a, b := nativeDetailFrame(first), nativeDetailFrame(second)
	if a.Details.Source != first.attached.presentation.View().Main || b.Details.Source != second.attached.presentation.View().Main {
		t.Fatal("detail publication lost its source runtime")
	}
	if a.Details == nil || b.Details == nil || a.Details.View.ID == b.Details.View.ID || a.Details.Output == nil || b.Details.Output == nil {
		t.Fatal("views shared or lost the selected detail/output")
	}
	if strings.Join(a.Details.Output.View.Lines, "\n") != "first output" || strings.Join(b.Details.Output.View.Lines, "\n") != "second output" {
		t.Fatal("detail outputs crossed clients")
	}
	foreign := a.Details.Output.DetailOutputRequest
	if refusal := second.applyView(sessionapi.NativeOperation{DetailOutput: &foreign}); refusal == "" {
		t.Fatal("foreign detail selection hydrated another view")
	}
	id := a.Details.View.ID
	if refusal := first.applyView(sessionapi.NativeOperation{CloseDetails: &id}); refusal != "" {
		t.Fatal(refusal)
	}
	if nativeDetailFrame(first).Details != nil || nativeDetailFrame(second).Details.View.ID != b.Details.View.ID {
		t.Fatal("closing one detail changed another selection")
	}
	n.retire("first")
	if nativeDetailFrame(second).Details.Output == nil || firstOutput.View().Released || secondOutput.View().Released || u.attached.session.Scope() == nil {
		t.Fatal("view disposal released shared execution/output ownership")
	}
}

func TestNativeFileDialogsOwnReadAndStaleCompletionLifetime(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", t.TempDir())
	for name, text := range map[string]string{"first.go": "package first\n", "second.go": "package second\n"} {
		if err := os.WriteFile(filepath.Join(u.attached.cwd, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	n, first, second := nativeDetailTestViews(t, u)
	first.openNoticeDetails("See first.go")
	before := first.attached.presentation.View()
	client := nativeClient{event: sessionapi.NativeEvent{Attachment: "first", Frame: sessionapi.NativeFrame{View: before}}, renderer: nativeClientRenderer{terminal: first.shell}}
	client.dialog(terminalsession.DialogIntent{Action: terminalsession.DialogOpenLink, Text: "first.go"})
	first.attached.feedback.Set("progress before link admission", false, time.Now())
	after := publishSessionView(&first.attached, &u.Client, &first.picker, "", preferences.Applied{}, u.serviceTiers, "", "", false, nil, true)
	if after.Version == before.Version || len(client.calls) != 1 {
		t.Fatal("fixture did not capture a link before publication advanced")
	}
	call := client.calls[0]
	request := sessionapi.NativeRequest{Attachment: call.target.Attachment, Thread: call.target.Thread, Version: call.target.Version, Workspace: u.attached.cwd, Context: t.Context(), Operation: call.operation}
	if _, refusal := n.admit(request, &u.attached, u.Thread); refusal != "" {
		t.Fatal(refusal)
	}
	if refusal := first.applyView(request.Operation); refusal != "" {
		t.Fatal(refusal)
	}
	if refusal := second.openDetails(sessionapi.NativeDetailRequest{Kind: sessionapi.NativeFileDetails, Path: "second.go"}); refusal != "" {
		t.Fatal(refusal)
	}
	firstResults, secondResults := first.files.fileReads, second.files.fileReads
	if firstResults == secondResults || first.files.fileReadSlot == second.files.fileReadSlot {
		t.Fatal("native views shared acquisition or capacity")
	}
	old := first.files.pendingFile.ID
	if refusal := first.openNoticeDetails("replacement detail"); refusal != "" {
		t.Fatal(refusal)
	}
	select {
	case stale := <-firstResults:
		if changed, _ := first.files.loadedMarkdownFile(&first.attached.session, first.attached.presentation, first.attached.cwd, first.shell, stale); changed {
			t.Fatal("stale file completion replaced a new dialog")
		}
	case <-time.After(time.Second):
		t.Fatal("canceled acquisition did not finish")
	}
	if first.shell.Dialog.ID == old {
		t.Fatal("replacement reused a detail identity")
	}
	n.retire("first")
	before = second.attached.presentation.View()
	if err := second.attached.presentation.Display(sessionpresentation.DisplayIntent{Version: before.Version, Thread: sessionpresentation.Thread{ID: "child", Caller: "/root/child"}}); err != nil {
		t.Fatal(err)
	}
	for second.files.fileReads != nil {
		select {
		case result := <-secondResults:
			if changed, _ := second.files.loadedMarkdownFile(&second.attached.session, second.attached.presentation, second.attached.cwd, second.shell, result); !changed {
				t.Fatal("closing first view canceled second read")
			}
		case <-time.After(time.Second):
			t.Fatal("second acquisition did not finish")
		}
	}
	frame := nativeDetailFrame(second)
	if frame.Details == nil || frame.Details.File == nil || frame.Details.File.Source != "package second\n" || frame.Details.File.Path != "second.go" {
		t.Fatal("file publication lost selected workspace/content")
	}
}

func TestNativeProcessDetailsBorrowExecutionCommands(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", "/workspace")
	_, view, _ := nativeDetailTestViews(t, u)
	view.files.openDetailSelection(&view.attached.session, view.attached.presentation.View().Version, view.attached.childSource, view.shell, view.shell.Agents, nil, true)
	processNotify(t, u, "main", "process", "printf running", "inProgress")
	frame := nativeDetailFrame(view)
	if frame.Details == nil || !frame.Details.View.Processes || len(frame.Details.View.Pages) != 1 || frame.Details.View.Pages[0].Code != "printf running" {
		t.Fatal("process dialog read a copied or empty command owner")
	}
	if frame.Details.View.Pages[0].Process != nil {
		t.Fatal("missing host identity enabled Stop")
	}
	processNotify(t, u, "main", "second", "sleep 30", "inProgress")
	appServerTestNotify(t, u, "item/commandExecution/terminalInteraction", map[string]any{"threadId": "main", "turnId": "t", "itemId": "second", "processId": "42"})
	frame = nativeDetailFrame(view)
	c := nativeTestPromptClient(u)
	c.accept(sessionapi.NativeEvent{Attachment: "first", Frame: frame})
	d := c.renderer.terminal.Dialog
	d.Paint(make([]string, 16), 80, 16)
	d.Key("\x1b[B")
	stop := d.Key("x")
	if stop.Process == nil || stop.Process.Item != "second" || stop.Process.ID != "42" {
		t.Fatal("queued navigation did not stop the selected process")
	}
	d.Key("\r")
	d.Key("/")
	if intent := d.Key("s"); intent.Action != 0 {
		t.Fatal("search triggered Stop")
	}
	d.Key("\x1b")
	processNotify(t, u, "main", "second", "sleep 30", "completed")
	c.accept(sessionapi.NativeEvent{Attachment: "first", Frame: nativeDetailFrame(view)})
	if intent := d.Key("s"); intent.Action != 0 {
		t.Fatal("completed process retained Stop")
	}
}

func TestNativeDetailInitialPageDoesNotOverrideClientNavigation(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", "/workspace")
	u.attached.source.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{
		{Seq: 1, Agent: "Main", Kind: "error", Text: "First failure"},
		{Seq: 2, Agent: "Main", Kind: "error", Text: "Newest failure"},
	}})
	_, view, _ := nativeDetailTestViews(t, u)
	if refusal := view.openDetails(sessionapi.NativeDetailRequest{Kind: sessionapi.NativeErrorDetails}); refusal != "" {
		t.Fatal(refusal)
	}
	frame := nativeDetailFrame(view)
	if frame.Details == nil || len(frame.Details.View.Pages) != 2 || frame.Details.Selected != frame.Details.View.Pages[1].ID {
		t.Fatal("new error dialog lost newest-page selection")
	}
	terminal, _ := u.terminalViews.NewAttachment(diffrender.DarkTheme)
	renderer := nativeClientRenderer{terminal: terminal, views: u.terminalViews}
	renderer.update(frame, conversation.DraftView{Available: true}, conversation.PromptDraftView{}, time.Now())
	if renderer.terminal.Dialog.Page != 1 {
		t.Fatal("native client did not use initial selected page identity")
	}
	renderer.terminal.Dialog.ShowPage(0)
	renderer.update(frame, conversation.DraftView{Available: true}, conversation.PromptDraftView{}, time.Now())
	if renderer.terminal.Dialog.Page != 0 {
		t.Fatal("publication overrode renderer-local page navigation")
	}
}

func TestNativeChildDialogLinkKeepsSourceAcrossNavigation(t *testing.T) {
	for _, destination := range []string{"main", "child-b"} {
		t.Run(destination, func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.startSession("main", "/parent-workspace")
			terminal, _ := u.terminalViews.NewAttachment(diffrender.DarkTheme)
			t.Cleanup(func() { _ = terminal.Diff.Close() })
			c := nativeClient{renderer: nativeClientRenderer{terminal: terminal, views: u.terminalViews}}
			view := u.attached.presentation.View()
			view.Displayed = sessionpresentation.Thread{ID: "child-a", Caller: "/root/child-a"}
			view.DisplayedWorkspace = "/child-workspace"
			detail := &sessionapi.NativeDetails{Source: view.Displayed, Agent: true, View: sessionpresentation.DetailView{
				ID:    sessionpresentation.DetailID{Attachment: view.Version.Attachment, Selection: 1},
				Pages: []sessionpresentation.DetailItem{{ID: "child-page"}},
			}}
			event := sessionapi.NativeEvent{Attachment: "view", Frame: sessionapi.NativeFrame{View: view, Details: detail}}
			c.accept(event)
			event.Frame.View.Displayed = sessionpresentation.Thread{ID: destination}
			event.Frame.View.DisplayedWorkspace = "/other-workspace"
			c.accept(event)
			c.dialog(terminalsession.DialogIntent{Action: terminalsession.DialogOpenLink, Text: "source.go"})
			if len(c.calls) != 1 || c.calls[0].operation.Details == nil {
				t.Fatalf("missing file detail request: %+v", c.calls)
			}
			request := c.calls[0].operation.Details
			if request.Displayed != detail.Source || request.Path != "source.go" {
				t.Fatalf("retained dialog link changed source: %+v", request)
			}
		})
	}
}

func TestNativeOutputDisclosureWithCapturedEdits(t *testing.T) {
	for _, captured := range []bool{false, true} {
		t.Run(fmt.Sprint(captured), func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			output := u.activity.Outputs(u.attached.session.Scope()).New()
			output.Finish(new("one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\n"), new(0))
			native := &sessionpresentation.NativeItem{Thread: "main", Item: "loop", Command: "printf output", Output: output, Exit: new(0)}
			if captured {
				native.CapturedEdit = &changehistory.Receipt{Thread: "main", Calls: []string{"loop"}, Pages: []activitydetail.Page{{ID: "edit:1", Verb: "Created", Path: "first.go", Code: "+first"}, {ID: "edit:2", Verb: "Created", Path: "second.go", Code: "+second"}}}
			}
			u.attached.source.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{Seq: 1, Agent: "Main", Kind: "tool", Text: "Run `printf output`", OutputTail: []string{"seven", "eight"}, OutputOmit: 6, Native: native}}})
			_, view, _ := nativeDetailTestViews(t, u)
			publishActivity(&view.attached, view.shell)
			pages := entryDetailPages(activitySource(&view.attached, view.shell, view.shell.Transcript), 1)
			if len(pages) == 0 {
				t.Fatal("missing activity pages")
			}
			page := pages[len(pages)-1]
			request := sessionapi.NativeDetailRequest{Kind: sessionapi.NativeEntryDetails, Source: page.Source, Operation: page.Operation, Output: page.Output}
			if refusal := view.openDetails(request); refusal != "" {
				t.Fatal(refusal)
			}
			frame := nativeDetailFrame(view)
			if frame.Details == nil || frame.Details.Output == nil || strings.Join(frame.Details.Output.View.Lines, "\n") != "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight" {
				t.Fatalf("output not selected: %+v", frame.Details)
			}
			request.Operation += 1000
			if refusal := view.openDetails(request); refusal == "" || nativeDetailFrame(view).Details.View.ID != frame.Details.View.ID {
				t.Fatal("stale operation replaced output")
			}
			if !captured {
				return
			}
			// The browser's source-only Details action retains both Activity and edits.
			if refusal := view.openDetails(sessionapi.NativeDetailRequest{Kind: sessionapi.NativeEntryDetails, Source: 1}); refusal != "" {
				t.Fatal(refusal)
			}
			row := nativeDetailFrame(view).Details
			if row == nil || len(row.View.Pages) < 3 || row.View.Pages[len(row.View.Pages)-1].ID != "edit:2" {
				t.Fatalf("source details lost captured edits: %+v", row)
			}
			for _, tc := range []struct{ path, selected string }{{"second.go", "edit:2"}, {"", "edit:1"}} {
				request = sessionapi.NativeDetailRequest{Kind: sessionapi.NativeEntryDetails, Source: 1, Edit: true, Path: tc.path}
				if refusal := view.openDetails(request); refusal != "" {
					t.Fatal(refusal)
				}
				detail := nativeDetailFrame(view).Details
				if detail == nil || len(detail.View.Pages) != 2 || detail.Selected != tc.selected {
					t.Fatalf("edit selection: %+v", detail)
				}
			}
			request.Path = "missing.go"
			before := nativeDetailFrame(view).Details.View.ID
			if refusal := view.openDetails(request); refusal == "" || nativeDetailFrame(view).Details.View.ID != before {
				t.Fatal("stale edit replaced selection")
			}
		})
	}
}

func TestNativeEditDetailsUseCapableCompanion(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", "/workspace")
	seq := u.attached.source.LastSeq + 1
	u.attached.source.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{
		{Seq: seq, Agent: "Main", Kind: "tool", Text: "Edit `a.go` +1 -1 · apply_patch\n\nEdit `b.go` +1 -1 · apply_patch", Native: &sessionpresentation.NativeItem{Thread: "main", Item: "edit", EditPages: []activity.Value{
			{Kind: "op", Verb: "Edit", Path: "a.go", Lang: "diff", Code: "@@ -1 +1 @@\n-wrong\n+other\n"},
			{Kind: "op", Verb: "Edit", Path: "b.go", Lang: "diff", Code: "@@ -1 +1 @@\n-before\n+exact\n"},
		}}},
	}})
	_, first, second := nativeDetailTestViews(t, u)
	request := sessionapi.NativeDetailRequest{Kind: sessionapi.NativeEntryDetails, Source: seq, Path: "b.go", Edit: true}
	first.viewerLease.DiffOpenExpires = time.Now().Add(time.Minute).UnixMilli()
	for id := uint64(1); id <= 2; id++ {
		if refusal := first.openDetails(request); refusal != "" {
			t.Fatal(refusal)
		}
		frame := nativeDetailFrame(first)
		if frame.Details != nil || frame.DiffOpen == nil || frame.DiffOpen.ID != id || frame.DiffOpen.Path != "b.go" || frame.DiffOpen.Source != "@@ -1 +1 @@\n-before\n+exact\n" {
			t.Fatalf("companion did not receive exact repeated selection: %+v", frame.DiffOpen)
		}
	}
	if second.diffOpen != nil {
		t.Fatal("selection crossed physical viewers")
	}
	first.attached.childSource.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{
		{Seq: seq, Agent: "worker", Kind: "tool", Text: "Edit `b.go` +1 -1 · apply_patch", Native: &sessionpresentation.NativeItem{Thread: "child", Item: "child-edit", EditPages: []activity.Value{
			{Kind: "op", Verb: "Edit", Path: "b.go", Lang: "diff", Code: "@@ -1 +1 @@\n-before\n+child\n"},
		}}},
	}})
	request.Agent = true
	if refusal := first.openDetails(request); refusal != "" {
		t.Fatal(refusal)
	}
	if frame := nativeDetailFrame(first); frame.Details != nil || frame.DiffOpen.ID != 3 || !strings.Contains(frame.DiffOpen.Source, "+child") {
		t.Fatal("child click used the main edit")
	}
	request.Agent = false
	for _, mode := range []string{"standalone", "expired", "remote"} {
		first.viewerLease.DiffOpenExpires = 0
		first.remoteHuman = mode == "remote"
		if mode == "expired" {
			first.viewerLease.DiffOpenExpires = time.Now().Add(-time.Second).UnixMilli()
		}
		if mode == "remote" {
			first.viewerLease.DiffOpenExpires = time.Now().Add(time.Minute).UnixMilli()
		}
		if refusal := first.openDetails(request); refusal != "" {
			t.Fatal(refusal)
		}
		frame := nativeDetailFrame(first)
		if frame.Details == nil || frame.DiffOpen == nil || frame.DiffOpen.ID != 3 {
			t.Fatalf("%s did not retain the dialog", mode)
		}
	}
}

func TestNativeMovedEditOpensCanonicalCompanionPath(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", "/workspace")
	seq := u.attached.source.LastSeq + 1
	const path = "snapshots/updated.txt"
	const display = "snapshots/{previous.txt.new=>updated.txt}"
	const code = "diff --git a/snapshots/previous.txt.new b/snapshots/updated.txt\nsimilarity index 100%\nrename from snapshots/previous.txt.new\nrename to snapshots/updated.txt\n"
	u.attached.source.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{
		{Seq: seq, Agent: "Main", Kind: "tool", Text: "Moved `" + display + "` +0 -0", Native: &sessionpresentation.NativeItem{
			Thread: "main", Item: "move", CapturedEdit: &changehistory.Receipt{Thread: "main", Calls: []string{"move"}, Pages: []activitydetail.Page{
				{ID: "move:1", Verb: "Move", Path: path, DisplayPath: display, Language: "diff", Code: code},
			}},
		}},
	}})
	_, view, _ := nativeDetailTestViews(t, u)
	view.viewerLease.DiffOpenExpires = time.Now().Add(time.Minute).UnixMilli()
	for _, selected := range []string{display, path} {
		if refusal := view.openDetails(sessionapi.NativeDetailRequest{Kind: sessionapi.NativeEntryDetails, Source: seq, Path: selected, Edit: true}); refusal != "" {
			t.Fatal(refusal)
		}
		frame := nativeDetailFrame(view)
		if frame.Details != nil || frame.DiffOpen == nil || frame.DiffOpen.Path != path || frame.DiffOpen.Source != code {
			t.Fatalf("move click did not preserve its canonical destination and original source: %+v", frame.DiffOpen)
		}
	}
}
