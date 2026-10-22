package launch

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversation "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

func TestNativeViewMenuRechecksTargetAfterStatusProgress(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", "/workspace")
	n := newNativeViews(t.Context(), sessionapi.NewLive().Native(), u.presentationState, u.terminalViews, u.activity, u.Requests)
	defer n.close()
	request := sessionapi.NativeRequest{Attachment: "view", Thread: "main", Workspace: "/workspace", Context: t.Context(), ViewContext: t.Context()}
	if refusal := n.attach(request, &u.attached, "main"); refusal != "" {
		t.Fatal(refusal)
	}
	v := n.views["view"]
	editor := new(composer.Editor)
	editor.InsertText("/status")
	v.completionTarget = conversation.EditorTarget{Revision: editor.Revision()}
	v.picker.refreshPicker(&v.attached, v.shell, u.clock, u.Requests, editor, t.Context(), nil)
	publish := func() sessionpresentation.View {
		return publishSessionView(&v.attached, &u.Client, &v.picker, "", &u.childHistoryState, preferences.Applied{}, nil, "", "", false, nil, time.Now(), true)
	}
	before := publish()
	if before.Menu == nil || len(before.Menu.Choices) != 1 {
		t.Fatalf("command completion missing: %+v", before.Menu)
	}
	request.Version = before.Version
	request.Operation = sessionapi.NativeOperation{Menu: &sessionpresentation.MenuAction{Version: before.Version, Menu: before.Menu.ID, Kind: sessionpresentation.MenuAccept, Choice: before.Menu.Choices[0].Identity()}, Editor: &sessionapi.NativeEditorInput{Target: v.completionTarget, Draft: editor.Draft.Clone()}}
	v.attached.feedback.Set("elapsed status advanced", false, time.Now())
	if after := publish(); after.Version == before.Version {
		t.Fatal("fixture did not advance publication")
	}
	if _, refusal := n.admit(request, &u.attached, "main"); refusal != "" {
		t.Fatal(refusal)
	}
	action, err := v.menuAction(request)
	if err != nil {
		t.Fatal(err)
	}
	applyMenuResult(&v.attached, v.shell, &u.Client, &v.picker, &editor, &u.childHistoryState, preferences.Applied{}, &u.serviceTiers, "", "", false, &u.side, u.clock, &u.questions, &u.approvals, false, nil, nil, t.Context(), nil, terminalsession.MenuResult{Action: action, Handled: true})
	if strings.TrimSpace(editor.Text) != "/status" {
		t.Fatal("admitted menu did not use existing command completion")
	}
	request.Operation.Editor.Target.Revision++
	if _, err := v.menuAction(request); err == nil {
		t.Fatal("changed local editor accepted an old completion")
	}
	request.Operation.Editor.Target = v.completionTarget
	v.picker.openMenu("archive")
	publish()
	if _, err := v.menuAction(request); err == nil {
		t.Fatal("replacement menu accepted an old completion")
	}
}

func TestNativeViewExplicitCompletionReopensDismissedTarget(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", "/workspace")
	n := newNativeViews(t.Context(), sessionapi.NewLive().Native(), u.presentationState, u.terminalViews, u.activity, u.Requests)
	defer n.close()
	if refusal := n.attach(sessionapi.NativeRequest{Attachment: "view", Thread: "main", Workspace: "/workspace", Context: t.Context(), ViewContext: t.Context()}, &u.attached, "main"); refusal != "" {
		t.Fatal(refusal)
	}
	v := n.views["view"]
	input := sessionapi.NativeEditorInput{Draft: composer.Draft{Text: "/"}}
	v.complete(input, u.clock, u.Requests, nil)
	if !v.picker.Open || len(v.picker.Choices) == 0 {
		t.Fatal("command choices did not open")
	}
	v.picker.Dismissed, v.picker.Open = v.picker.Target, false
	v.complete(input, u.clock, u.Requests, nil)
	if v.picker.Open {
		t.Fatal("passive completion reopened a dismissed target")
	}
	input.Explicit = true
	v.complete(input, u.clock, u.Requests, nil)
	if !v.picker.Open || len(v.picker.Choices) == 0 {
		t.Fatal("explicit completion did not reopen command choices")
	}
}

func TestNativeViewReplacementRetiresAcquisitions(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", "/workspace")
	n := newNativeViews(t.Context(), sessionapi.NewLive().Native(), u.presentationState, u.terminalViews, u.activity, u.Requests)
	defer n.close()
	for _, id := range []string{"origin", "other"} {
		if refusal := n.attach(sessionapi.NativeRequest{Attachment: id, Thread: "main", Workspace: "/workspace", Context: t.Context(), ViewContext: t.Context()}, &u.attached, "main"); refusal != "" {
			t.Fatal(refusal)
		}
	}
	v, other := n.views["origin"], n.views["other"]
	oldModel, oldPicker, execution := v.attached.presentation, v.picker.State, u.attached.executionPresentation
	scan, cancel := context.WithCancel(t.Context())
	v.picker.scanCancel = cancel
	v.picker.openMenu("side-replace")
	v.picker.sideTask = &composer.Draft{Text: "prepared oneoff"}
	v.panel = &appServerStatusReport{}
	v.resume = &appServerResumePicker{}
	read, err := u.Requests.Send("thread/read", nil, viewRequestScope("origin"), appServerRequest{view: "origin"})
	if err != nil {
		t.Fatal(err)
	}
	// The canonical restoration owner moves its post-rewind confirmation to the
	// originating live view, even when its replacement has not published yet.
	n.replacing = "origin"
	u.attached.rewindArchive = n.rewindArchive
	u.Startup.RewindOriginal = "main"
	state := sessionpresentation.NewRestoration("branch")
	state.EndDiscovery()
	state.ContentRendered()
	u.restoring = &appServerActivityRestore{state: state}
	if err := u.finishRestoredContent(); err != nil {
		t.Fatal(err)
	}
	if v.archiveOriginal != "main" || other.archiveOriginal != "" || u.picker.Kind == "archive" || u.Startup.RewindOriginal != "" {
		t.Fatal("post-rewind confirmation crossed its originating view")
	}
	n.replacePresentation(v, &u.attached)
	v.attached.presentation.Attach(sessionpresentation.Thread{ID: "branch", Caller: "/root"}, false)
	if scan.Err() == nil || v.picker.State == oldPicker || v.attached.presentation == oldModel || v.panel != nil || v.resume != nil || v.picker.Open || v.picker.sideTask != nil {
		t.Fatal("replacement retained old view acquisitions")
	}
	if _, ok := u.Requests.Lookup(read); ok {
		t.Fatal("replacement retained old view response correlation")
	}
	if v.attached.executionPresentation != execution || other.attached.executionPresentation != execution || len(v.effects.pending) != 1 || v.effects.pending[0].Restore.Text != "prepared oneoff" {
		t.Fatal("replacement copied execution or discarded the local pending input")
	}
	showRewindArchive(&v.picker, v.archiveOriginal)
	publication := publishSessionView(&v.attached, &u.Client, &v.picker, "", &u.childHistoryState, preferences.Applied{}, nil, "", "", false, nil, time.Now(), true)
	if publication.Menu == nil || publication.Menu.Kind != sessionpresentation.MenuArchive || v.picker.archiveTarget != "main" || other.picker.Open {
		t.Fatal("post-rewind confirmation was not published by its own view")
	}
}

func TestNativeViewRestorationUsesOriginatingEditorLifetime(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", "/workspace")
	n := newNativeViews(t.Context(), sessionapi.NewLive().Native(), u.presentationState, u.terminalViews, u.activity, u.Requests)
	defer n.close()
	for _, id := range []string{"first", "second"} {
		if refusal := n.attach(sessionapi.NativeRequest{Attachment: id, Thread: "main", Workspace: "/workspace", Context: t.Context(), ViewContext: t.Context()}, &u.attached, "main"); refusal != "" {
			t.Fatal(refusal)
		}
	}
	u.attached.restoreInput = n.restore
	u.Editor.Text = "legacy draft"
	first, second := n.views["first"], n.views["second"]
	restoreDrafts(&u.attached, u.shell, &u.questions, &u.approvals, &u.picker, "", u.clock,
		conversation.Input{Origin: "first", Delivery: first.effects.input(), Text: "first input"},
		conversation.Input{Origin: "first", Delivery: first.effects.input(), Text: "next input"},
		conversation.Input{Origin: "second", Text: "second input"})
	if u.Editor.Text != "legacy draft" || len(first.effects.pending) != 2 || first.effects.reserved != 0 || len(first.effects.inputs) != 0 || len(second.effects.pending) != 1 || second.effects.pending[0].Restore.Text != "second input" {
		t.Fatal("restoration crossed editor ownership")
	}
	c := nativeTestPromptClient(u)
	c.editor("main").Replace(composer.Draft{Text: "new typing"})
	event := sessionapi.NativeEvent{Attachment: "client", Frame: sessionapi.NativeFrame{View: u.attached.presentation.View(), Effects: first.effects.publication()}}
	c.accept(event)
	c.accept(event)
	if c.editor("main").Text != "first input\nnext input\nnew typing" {
		t.Fatal("individual restoration changed input order or duplicated delivery")
	}
	n.retire("first")
	restoreDrafts(&u.attached, u.shell, &u.questions, &u.approvals, &u.picker, "", u.clock, conversation.Input{Origin: "first", Text: "late rejected input"})
	if len(second.effects.pending) != 1 || u.Editor.Text != "legacy draft" || len(u.attached.conversation.Unsent) != 0 || len(u.attached.conversation.Queued) != 0 {
		t.Fatal("closed editor restoration retargeted or retried input")
	}
	if refusal := second.applyView(sessionapi.NativeOperation{Receipt: &sessionapi.NativeReceipt{Effect: second.effects.pending[0].ID}}); refusal != "" || len(second.effects.pending) != 0 {
		t.Fatalf("receipt did not retire only delivered effects: %s", refusal)
	}
}

func TestNativeViewCommandBorrowsExecutionWithoutSharedDraft(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", "/workspace")
	n := newNativeViews(t.Context(), sessionapi.NewLive().Native(), u.presentationState, u.terminalViews, u.activity, u.Requests)
	defer n.close()
	if refusal := n.attach(sessionapi.NativeRequest{Attachment: "view", Thread: "main", Workspace: "/workspace", Context: t.Context(), ViewContext: t.Context()}, &u.attached, "main"); refusal != "" {
		t.Fatal(refusal)
	}
	v := n.views["view"]
	root := u.attached
	root.conversation.Editor.Text = "unrelated editor"
	u.attached, u.shell, u.picker = v.attached, v.shell, v.picker
	u.Editor = &composer.Editor{Text: "/status"}
	view := publishSessionView(&u.attached, &u.Client, &u.picker, "", &u.childHistoryState, preferences.Applied{}, u.serviceTiers, "", "", false, nil, time.Now(), true)
	_, err := u.applyComposerInput(terminalsession.ComposerInput{Command: sessionpresentation.ComposerIntent{Version: view.Version, Thread: "main", Target: conversation.EditorTarget{Revision: 99}, Operation: sessionpresentation.SubmitComposer}})
	if err != nil {
		t.Fatal(err)
	}
	if root.conversation.Editor.Text != "unrelated editor" || u.Editor.Text != "" || u.Editor == &root.conversation.Editor || u.statusPanel == nil || root.presentation.View().Panel != nil {
		t.Fatal("native command consumed or opened another view's state")
	}
	request, ok := u.Requests.Lookup("1")
	if !ok || request.Owner.view != "view" || request.Owner.status != u.statusPanel || request.Scope != viewRequestScope("view") {
		t.Fatalf("status acquisition has no originating view: %+v", request)
	}
	v.panel = u.statusPanel
	current := publishSessionView(&v.attached, &u.Client, &v.picker, "", &u.childHistoryState, preferences.Applied{}, nil, "", "", false, nil, time.Now(), true)
	close := sessionapi.NativeRequest{Attachment: "view", Thread: "main", Workspace: "/workspace", Context: t.Context(), Version: current.Version, Operation: sessionapi.NativeOperation{Panel: &sessionpresentation.PanelAction{Version: current.Version, Panel: current.Panel.ID, Kind: sessionpresentation.PanelClose}}}
	v.attached.feedback.Set("account status advanced", false, time.Now())
	publishSessionView(&v.attached, &u.Client, &v.picker, "", &u.childHistoryState, preferences.Applied{}, nil, "", "", false, nil, time.Now(), true)
	if _, refusal := n.admit(close, &u.attached, "main"); refusal != "" {
		t.Fatal(refusal)
	}
	action, err := v.panelAction(close)
	if err != nil {
		t.Fatal(err)
	}
	applyPanelAction(&v.attached, v.shell, u.clock, &v.panel, nil, action)
	after := publishSessionView(&v.attached, &u.Client, &v.picker, "", &u.childHistoryState, preferences.Applied{}, nil, "", "", false, nil, time.Now(), true)
	if after.Panel != nil || v.panel != nil {
		t.Fatal("status progress prevented identified panel close")
	}
	if _, err := v.panelAction(close); err == nil {
		t.Fatal("closed panel admitted a late action")
	}
	other, err := u.Requests.Send("thread/read", nil, "main", appServerRequest{history: true})
	if err != nil {
		t.Fatal(err)
	}
	n.retire("view")
	if _, ok := u.Requests.Lookup("1"); ok {
		t.Fatal("retired view retained its status acquisition")
	}
	if _, ok := u.Requests.Lookup(other); !ok {
		t.Fatal("view retirement removed execution/history correlation")
	}
}

func TestNativeViewObservedReceiptKeepsCapturedIdentity(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", "/workspace")
	n := newNativeViews(t.Context(), sessionapi.NewLive().Native(), u.presentationState, u.terminalViews, u.activity, u.Requests)
	defer n.close()
	request := sessionapi.NativeRequest{Attachment: "view", Thread: "main", Workspace: "/workspace", Context: t.Context(), ViewContext: t.Context()}
	if refusal := n.attach(request, &u.attached, "main"); refusal != "" {
		t.Fatal(refusal)
	}
	v := n.views["view"]
	before := publishSessionView(&v.attached, &u.Client, &v.picker, "", &u.childHistoryState, preferences.Applied{}, u.serviceTiers, "", "", false, nil, time.Now(), true)
	v.attached.feedback.Set("new progress", false, time.Now())
	after := publishSessionView(&v.attached, &u.Client, &v.picker, "", &u.childHistoryState, preferences.Applied{}, u.serviceTiers, "", "", false, nil, time.Now(), true)
	if before.Version == after.Version {
		t.Fatal("fixture did not advance its publication")
	}
	request.Version = before.Version
	request.Operation.Receipt = &sessionapi.NativeReceipt{}
	if _, refusal := n.admit(request, &u.attached, "main"); refusal != "" {
		t.Fatalf("older observed receipt refused: %s", refusal)
	}
	request.Operation = sessionapi.NativeOperation{Command: new("/status")}
	if _, refusal := n.admit(request, &u.attached, "main"); refusal != "" {
		t.Fatal("progress invalidated a command in the same destination:", refusal)
	}
	request.Operation = sessionapi.NativeOperation{Receipt: &sessionapi.NativeReceipt{}}
	request.Workspace = "/replacement"
	if _, refusal := n.admit(request, &u.attached, "main"); refusal == "" {
		t.Fatal("receipt crossed captured workspace identity")
	}
	v.effects.add(sessionapi.NativeEffect{Restore: &composer.Draft{Text: "waiting input"}})
	v.notices = &nativeCriticalNoticeDelivery{attachment: before.Version.Attachment}
	if refusal := v.applyView(sessionapi.NativeOperation{Receipt: &sessionapi.NativeReceipt{Effect: v.effects.next, Notice: sessionapi.NativeNoticeReceipt{Attachment: before.Version.Attachment + 1}}}); refusal == "" || len(v.effects.pending) != 1 {
		t.Fatal("mixed stale notice receipt acknowledged editor restoration")
	}
}

func TestNativePreparedViewAdmissionKeepsAttachmentIdentity(t *testing.T) {
	for _, answers := range []bool{false, true} {
		t.Run(map[bool]string{false: "composer", true: "answers"}[answers], func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.startSession("main", "/workspace")
			n := newNativeViews(t.Context(), sessionapi.NewLive().Native(), u.presentationState, u.terminalViews, u.activity, u.Requests)
			defer n.close()
			request := sessionapi.NativeRequest{Attachment: "view", Thread: "main", Workspace: "/workspace", Context: t.Context(), ViewContext: t.Context()}
			if refusal := n.attach(request, &u.attached, "main"); refusal != "" {
				t.Fatal(refusal)
			}
			v := n.views["view"]
			publish := func() sessionpresentation.View {
				return publishSessionView(&v.attached, &u.Client, &v.picker, "", &u.childHistoryState, preferences.Applied{}, u.serviceTiers, "", "", false, nil, time.Now(), true)
			}
			request.Version = publish().Version
			request.Prepared = sessionapi.NewPreparedInput(composer.Draft{Text: "acquired snapshot"}, nil)
			defer request.Prepared.Cleanup()
			if answers {
				request.Operation.Answers = &sessionapi.NativeAnswers{}
			} else {
				request.Operation.Composer = &sessionapi.NativeComposerInput{Intent: sessionpresentation.ComposerIntent{Operation: sessionpresentation.SubmitComposer}}
			}
			v.attached.feedback.Set("progress during acquisition", false, time.Now())
			if after := publish(); after.Version == request.Version {
				t.Fatal("fixture did not advance publication")
			}
			if _, refusal := n.admit(request, &u.attached, "main"); refusal != "" {
				t.Fatalf("same-attachment progress refused prepared input: %s", refusal)
			}
			n.retire("view")
			if refusal := n.attach(request, &u.attached, "main"); refusal != "" {
				t.Fatal(refusal)
			}
			v = n.views["view"]
			if v.attached.presentation.View().Version.Attachment == request.Version.Attachment {
				t.Fatal("fixture did not replace attachment")
			}
			if _, refusal := n.admit(request, &u.attached, "main"); refusal == "" {
				t.Fatal("prepared input crossed replacement attachment")
			}
		})
	}
}

func TestNativeViewResumeUsesExistingExecutionOwner(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", "/workspace")
	n := newNativeViews(t.Context(), sessionapi.NewLive().Native(), u.presentationState, u.terminalViews, u.activity, u.Requests)
	defer n.close()
	if refusal := n.attach(sessionapi.NativeRequest{Attachment: "view", Thread: "main", Workspace: "/workspace", Context: t.Context(), ViewContext: t.Context()}, &u.attached, "main"); refusal != "" {
		t.Fatal(refusal)
	}
	v := n.views["view"]
	u.attached, u.shell, u.picker = v.attached, v.shell, v.picker
	u.attached.activeThread = func(id string) bool { return id == "other" }
	u.Editor = &composer.Editor{Text: "/resume other"}
	view := publishSessionView(&u.attached, &u.Client, &u.picker, "", &u.childHistoryState, preferences.Applied{}, u.serviceTiers, "", "", false, nil, time.Now(), true)
	if _, err := u.applyComposerInput(terminalsession.ComposerInput{Command: sessionpresentation.ComposerIntent{Version: view.Version, Thread: "main", Operation: sessionpresentation.SubmitComposer}}); err != nil {
		t.Fatal(err)
	}
	if wire.Len() != 0 || u.Replacement.Pending() || len(v.effects.pending) != 1 || *v.effects.pending[0].Reattach != "other" {
		t.Fatal("live /resume issued another host resume or replaced source execution")
	}
	if err := openResumePicker(&u.attached, &u.Client, &u.picker, u.clock, &u.resumePicker, false); err != nil {
		t.Fatal(err)
	}
	u.resumePicker.Append([]sessionpresentation.ResumeRow{{ID: "other", Workspace: "/other"}}, "", 1)
	view = publishSessionView(&u.attached, &u.Client, &u.picker, "", &u.childHistoryState, preferences.Applied{}, u.serviceTiers, "", "", false, nil, time.Now(), true)
	before, picker := wire.Len(), u.resumePicker
	if err := applyResumeAction(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, preferences.Applied{}, u.serviceTiers, "", "", &u.restoring, &u.side, &u.statusPanel, &u.resumePicker, false, &u.quitRequested, sessionpresentation.ResumeAction{Version: view.Version, Picker: view.Resume.ID, Kind: sessionpresentation.ResumeChoose, Thread: "other"}); err != nil {
		t.Fatal(err)
	}
	if wire.Len() != before || u.Replacement.Pending() || u.resumePicker != picker || len(v.effects.pending) != 2 || *v.effects.pending[1].Reattach != "other" {
		t.Fatal("chooser lost source view before destination attachment")
	}
}
