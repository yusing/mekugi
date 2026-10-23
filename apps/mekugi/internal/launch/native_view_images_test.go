package launch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversation "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

func TestNativeViewQueuedImageClaimsEndAtReceiptOrDisconnect(t *testing.T) {
	for _, disconnect := range []bool{false, true} {
		t.Run(map[bool]string{false: "receipt", true: "disconnect"}[disconnect], func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.startSession("main", "/workspace")
			n := newNativeViews(t.Context(), sessionapi.NewLive().Native(), u.presentationState, u.terminalViews, u.activity, u.Requests)
			defer n.close()
			if refusal := n.attach(sessionapi.NativeRequest{Attachment: "view", Thread: "main", Workspace: "/workspace", Context: t.Context(), ViewContext: t.Context()}, &u.attached, "main"); refusal != "" {
				t.Fatal(refusal)
			}
			u.attached.nativeImages, u.attached.restoreInput = n.imageClaims, n.restore
			prune := func() {
				pruneDraftImages(u.ownedImages, u.Editor, u.attached.conversation, &u.questions, &u.approvals, u.shell.Prompts, u.side, n.imageClaims)
			}
			n.retiring = func(*nativeSessionView) error { prune(); return nil }
			path, borrowed := filepath.Join(t.TempDir(), "owned.png"), pasteTestImage(t)
			if err := os.WriteFile(path, []byte("owned fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			u.ownedImages = map[string]bool{path: true}
			u.attached.conversation.QueueInput(conversation.Input{Origin: "view", Text: "[Image 1] [Image 2]", Images: []composer.Token{{Start: 0, End: 9, Path: path}, {Start: 10, End: 19, Path: borrowed}}}, true)
			editQueued(&u.attached, u.shell, &u.Client, &u.picker, &u.questions, &u.approvals, u.clock)
			prune()
			if _, err := os.Stat(path); err != nil {
				t.Fatal("withdrawal lost image before client copied it:", err)
			}
			view := n.views["view"]
			if disconnect {
				n.retire("view")
			} else {
				id := view.effects.pending[0].ID
				if refusal := view.applyView(sessionapi.NativeOperation{Receipt: &sessionapi.NativeReceipt{Effect: id}}); refusal != "" {
					t.Fatal(refusal)
				}
				prune()
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("retired claim kept unsubmitted image: %v", err)
			}
			if _, err := os.Stat(borrowed); err != nil {
				t.Fatal("image claims conferred deletion authority over a borrowed path:", err)
			}
		})
	}
}

func TestNativeSidePreparedSnapshotsAndPendingReplacementClaims(t *testing.T) {
	u, wire := newAppServerTestUI()
	workspace := t.TempDir()
	u.startSession("main", workspace)
	n := newNativeViews(t.Context(), sessionapi.NewLive().Native(), u.presentationState, u.terminalViews, u.activity, u.Requests)
	defer n.close()
	if refusal := n.attach(sessionapi.NativeRequest{Attachment: "view", Thread: "main", Workspace: workspace, Context: t.Context(), ViewContext: t.Context()}, &u.attached, "main"); refusal != "" {
		t.Fatal(refusal)
	}
	v := n.views["view"]
	u.attached.nativeImages = n.imageClaims
	u.side = &sessioncontrol.Side{SideAnswer: &conversation.SideAnswer{Key: "existing"}, Thread: "side"}
	u.attached.conversation.Side = u.side.SideAnswer
	file, image := filepath.Join(workspace, "source.txt"), pasteTestImage(t)
	if err := os.WriteFile(file, []byte("captured contents"), 0600); err != nil {
		t.Fatal(err)
	}
	draft := composer.Draft{Text: "@source.txt [Image 1]", Files: []composer.Token{{Start: 0, End: 11, Path: "source.txt"}}, Images: []composer.Token{{Start: 12, End: 21, Path: image}}}
	prepare := func(draft composer.Draft) *sessionapi.PreparedInput {
		t.Helper()
		prepared, err := prepareAPIInput(t.Context(), workspace, sessionapi.InputRequest{NativeDraft: &draft})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(prepared.Cleanup)
		for _, image := range prepared.Draft.Images {
			t.Cleanup(func() { _ = os.Remove(image.Path) })
		}
		return prepared
	}
	submit := func(prepared *sessionapi.PreparedInput) *composer.Editor {
		t.Helper()
		editor := new(composer.Editor)
		editor.Replace(prepared.Draft)
		if err := submitBTW(&v.attached, v.shell, &u.Client, &v.picker, &editor, &u.questions, &u.approvals, u.clock, preferences.Applied{}, u.serviceTiers, "", "", false, &u.side, false, &u.ownedImages, nil, &u.guardHookCheck, u.controlObservations, &composerPreparation{ctx: t.Context(), input: prepared}); err != nil {
			t.Fatal(err)
		}
		return editor
	}
	refused := prepare(draft)
	u.side.Busy = true
	if editor := submit(refused); editor.Text == "" || refused.Admitted() || wire.Len() != 0 {
		t.Fatal("busy side consumed or admitted a prepared request")
	}
	refused.Cleanup()
	u.side.Busy = false
	prepared := prepare(draft)
	if err := os.WriteFile(file, []byte("changed after preparation"), 0600); err != nil {
		t.Fatal(err)
	}
	if editor := submit(prepared); editor.Text != "" || !prepared.Admitted() || !strings.Contains(wire.String(), "captured contents") || strings.Contains(wire.String(), "changed after preparation") {
		t.Fatalf("side submission reread or lost preparation: %s", wire.String())
	}
	// A pending replacement owns its copied image before any replacement host
	// operation, and cancelling that chooser releases only the server copy.
	u.side.Busy, u.side.Starting = false, false
	oneoff := composer.Join(composer.Draft{Text: "/oneoff"}, draft)
	replacement := prepare(oneoff)
	editor := submit(replacement)
	copy := replacement.Draft.Images[0].Path
	pruneDraftImages(u.ownedImages, u.Editor, u.attached.conversation, &u.questions, &u.approvals, u.shell.Prompts, u.side, n.imageClaims)
	if !replacement.Admitted() || v.picker.sideTask == nil {
		t.Fatal("replacement lost its admitted pending payload")
	}
	if _, err := os.Stat(copy); err != nil {
		t.Fatal("pending replacement image was reclaimed:", err)
	}
	view := publishSessionView(&v.attached, &u.Client, &v.picker, "", preferences.Applied{}, u.serviceTiers, "", "", false, u.side, time.Now(), true)
	action := sessionpresentation.MenuAction{Version: view.Version, Menu: view.Menu.ID, Kind: sessionpresentation.MenuAccept, Choice: view.Menu.Choices[0].Identity()}
	applyMenuResult(&v.attached, v.shell, &u.Client, &v.picker, &editor, preferences.Applied{}, &u.serviceTiers, "", "", false, &u.side, u.clock, &u.questions, &u.approvals, false, u.ownedImages, u.panes, t.Context(), nil, terminalsession.MenuResult{Action: action, Handled: true})
	pruneDraftImages(u.ownedImages, u.Editor, u.attached.conversation, &u.questions, &u.approvals, u.shell.Prompts, u.side, n.imageClaims)
	if v.picker.sideTask != nil {
		t.Fatal("cancel kept pending replacement state")
	}
	if _, err := os.Stat(copy); !os.IsNotExist(err) {
		t.Fatalf("cancel kept copied replacement image: %v", err)
	}
	if _, err := os.Stat(image); err != nil {
		t.Fatal("cancel reclaimed client source:", err)
	}
	confirmed := prepare(oneoff)
	if err := os.WriteFile(file, []byte("changed after replacement preparation"), 0600); err != nil {
		t.Fatal(err)
	}
	editor = submit(confirmed)
	previous := u.side
	view = publishSessionView(&v.attached, &u.Client, &v.picker, "", preferences.Applied{}, u.serviceTiers, "", "", false, u.side, time.Now(), true)
	action = sessionpresentation.MenuAction{Version: view.Version, Menu: view.Menu.ID, Kind: sessionpresentation.MenuAccept, Choice: view.Menu.Choices[1].Identity()}
	applyMenuResult(&v.attached, v.shell, &u.Client, &v.picker, &editor, preferences.Applied{}, &u.serviceTiers, "", "", false, &u.side, u.clock, &u.questions, &u.approvals, false, u.ownedImages, u.panes, t.Context(), nil, terminalsession.MenuResult{Action: action, Handled: true})
	pruneDraftImages(u.ownedImages, u.Editor, u.attached.conversation, &u.questions, &u.approvals, u.shell.Prompts, u.side, n.imageClaims)
	if u.side == previous || u.side.Pending.Origin != "view" || v.picker.sideTask != nil || strings.Contains(strings.Join(u.side.Pending.Attachments, ""), "changed after replacement preparation") {
		t.Fatal("confirmed replacement lost origin or re-acquired its snapshot")
	}
	if _, err := os.Stat(confirmed.Draft.Images[0].Path); err != nil {
		t.Fatal("admitted side pending input lost its image claim:", err)
	}
}
