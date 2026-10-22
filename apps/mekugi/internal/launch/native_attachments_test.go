package launch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

func TestNativeAttachmentPreparationOwnsCopiesAndReusesText(t *testing.T) {
	workspace := t.TempDir()
	file := filepath.Join(workspace, "source.txt")
	if err := os.WriteFile(file, []byte("captured contents"), 0600); err != nil {
		t.Fatal(err)
	}
	image := pasteTestImage(t)
	draft := composer.Draft{Text: "@source.txt [Image 1]", Files: []composer.Token{{Start: 0, End: 11, Path: "source.txt"}}, Images: []composer.Token{{Start: 12, End: 21, Path: image}}}
	// The already prepared bytes survive subsequent edits to the host source.
	draft.SnapshotFiles(workspace)
	if err := os.WriteFile(file, []byte("later contents"), 0600); err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareAPIInput(t.Context(), workspace, sessionapi.InputRequest{NativeDraft: &draft})
	if err != nil {
		t.Fatal(err)
	}
	copy := prepared.Draft.Images[0].Path
	defer os.Remove(copy)
	if copy == image || draft.Images[0].Path != image || !strings.Contains(strings.Join(prepared.Draft.Attachments, ""), "captured contents") || strings.Contains(strings.Join(prepared.Draft.Attachments, ""), "later contents") {
		t.Fatalf("native preparation changed source ownership or reread snapshots: %+v", prepared.Draft)
	}
	prepared.Cleanup()
	if _, err := os.Stat(copy); !os.IsNotExist(err) {
		t.Fatalf("unadmitted image copy survived cleanup: %v", err)
	}
	if _, err := os.Stat(image); err != nil {
		t.Fatalf("client image was reclaimed: %v", err)
	}
	prepared, err = prepareAPIInput(t.Context(), workspace, sessionapi.InputRequest{NativeDraft: &draft})
	if err != nil {
		t.Fatal(err)
	}
	copy = prepared.Draft.Images[0].Path
	defer os.Remove(copy)
	owned := make(map[string]bool)
	if !prepared.Admit(t.Context(), owned) || !owned[copy] || owned[image] {
		t.Fatalf("native admission ownership: %v", owned)
	}
	prepared.Cleanup()
	if _, err := os.Stat(copy); err != nil {
		t.Fatalf("admitted copy did not survive request cleanup: %v", err)
	}
}

func TestNativeAttachmentRejectsInvalidSourceSpans(t *testing.T) {
	draft := composer.Draft{Text: "selection", Selections: []composer.Token{{Start: -1, End: 9, Text: "selected bytes"}}}
	if prepared, err := prepareNativeDraft(t.Context(), t.TempDir(), draft); err == nil || prepared != nil {
		t.Fatalf("invalid client span reached snapshot slicing: %+v, %v", prepared, err)
	}
}
