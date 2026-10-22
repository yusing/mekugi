package launch

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversation "github.com/yusing/mekugi/modules/conversation/presentation"
)

func TestRemoteRecoveryReceiptPreservesImageClaims(t *testing.T) {
	for _, disconnect := range []bool{false, true} {
		t.Run(map[bool]string{false: "discard", true: "disconnect"}[disconnect], func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.startSession("main", t.TempDir())
			h := remoteOwnerView(t, u)
			prune := func() {
				pruneDraftImages(u.ownedImages, u.Editor, u.attached.conversation, &u.questions, &u.approvals, u.shell.Prompts, u.side, h.views.imageClaims)
			}
			h.views.retiring = func(*nativeSessionView) error { prune(); return nil }
			path := filepath.Join(t.TempDir(), "owned.png")
			if err := os.WriteFile(path, []byte("owned fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			borrowed := pasteTestImage(t)
			u.ownedImages = map[string]bool{path: true}
			draft := composer.Draft{Text: "[Image 1]", Images: []composer.Token{{Start: 0, End: 9, Path: path}}, AnswerImages: []string{borrowed}}
			h.views.restore(u.attached.executionPresentation, []conversation.Input{{Origin: h.event.Attachment, Draft: draft}})
			id := h.view.effects.pending[0].ID
			h.view.effects.add(sessionapi.NativeEffect{Thread: "main", Clipboard: new("ordinary delivery")})
			last := h.view.effects.pending[1].ID
			code, reason := h.send(sessionapi.NativeOperation{Receipt: &sessionapi.NativeReceipt{Effect: last}}, func(request sessionapi.NativeRequest) string {
				return h.view.applyView(request.Operation)
			})
			if code != 202 {
				t.Fatalf("receipt: %d %s", code, reason)
			}
			prune()
			if _, err := os.Stat(path); err != nil {
				t.Fatal("remote receipt released retained image:", err)
			}
			if len(h.view.effects.pending) != 1 || h.view.effects.recovery(id) == nil || !reflect.DeepEqual(*h.view.effects.recovery(id).Restore, draft) {
				t.Fatal("receipt lost recovery or retained ordinary effect")
			}
			if disconnect {
				h.views.retire(h.event.Attachment)
			} else {
				h.view.effects.discardRecovery(id)
				prune()
				if h.view.effects.recovery(id) != nil {
					t.Fatal("discard left consumable recovery")
				}
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("ended recovery retained owned image: %v", err)
			}
			if _, err := os.Stat(borrowed); err != nil {
				t.Fatal("recovery cleanup deleted borrowed image:", err)
			}
		})
	}
}

func TestNativeRecoveryJoinedSnapshotsAndExactConsumption(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", t.TempDir())
	h := remoteOwnerView(t, u)
	first := composer.Draft{Text: "@file", Files: []composer.Token{{Start: 0, End: 5, Path: "file"}}, Attachments: []string{"original file contents"}}
	second := composer.Draft{Text: "@skill", Skills: []composer.Token{{Start: 0, End: 6, Name: "skill"}}, Attachments: []string{"original skill contents"}}
	h.views.restore(u.attached.executionPresentation, []conversation.Input{{Origin: h.event.Attachment, Draft: first}, {Origin: h.event.Attachment, Draft: second}, {Origin: "detached", Draft: composer.Draft{Text: "foreign"}}})
	if len(h.view.effects.pending) != 1 {
		t.Fatal("restore did not join originating submissions")
	}
	id := h.view.effects.pending[0].ID
	want := composer.Draft{Text: "@file\n@skill", Files: []composer.Token{{Start: 0, End: 5, Path: "file"}}, Skills: []composer.Token{{Start: 6, End: 12, Name: "skill"}}, Attachments: []string{"original file contents", "original skill contents"}}
	if got := h.view.effects.recovery(id); got == nil || !reflect.DeepEqual(*got.RecoveryDraft(), want) {
		t.Fatalf("joined recovery lost immutable snapshots: %+v", got)
	}
	h.views.restoreShell(u.attached.executionPresentation, conversation.Input{Origin: h.event.Attachment, Draft: composer.Draft{Text: "pwd"}})
	other := h.view.effects.pending[1].ID
	h.view.effects.discardRecovery(id)
	if h.view.effects.recovery(id) != nil || h.view.effects.recovery(other) == nil {
		t.Fatal("consumption affected wrong recovery")
	}
	h.view.effects.discardRecovery(id)
	if len(h.view.effects.pending) != 1 || h.view.effects.pending[0].ID != other {
		t.Fatal("duplicate consumption affected sibling recovery")
	}
	// The native client still releases restoration after copying it locally.
	h.view.remoteHuman = false
	if refusal := h.view.applyView(sessionapi.NativeOperation{Receipt: &sessionapi.NativeReceipt{Effect: other}}); refusal != "" || len(h.view.effects.pending) != 0 {
		t.Fatal("native receipt retained delivered restoration", refusal)
	}
}
