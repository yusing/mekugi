package launch

import (
	"encoding/base64"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
)

func checkNativePathCopy(t *testing.T, workspace, target, want string, retire bool) {
	t.Helper()
	u, _ := newAppServerTestUI()
	u.startSession("main", workspace)
	_, view, _ := nativeDetailTestViews(t, u)
	if refusal := view.openDetails(sessionapi.NativeDetailRequest{Kind: sessionapi.NativeFileDetails, Path: target}); refusal != "" {
		t.Fatal(refusal)
	}
	if view.shell.Dialog != nil {
		t.Fatal("nonregular path opened a dialog before acquisition")
	}
	results := view.files.fileReads
	if retire {
		view.files.closeOutput(view.shell)
	}
	select {
	case result := <-results:
		// Put the actual acquisition back through native publication and its effect consumer.
		ready := make(chan markdownFileResult, 1)
		ready <- result
		view.files.fileReads = ready
		frame := nativeDetailFrame(view)
		if frame.Details != nil || view.shell.Dialog != nil {
			t.Fatal("nonregular path published a dialog")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nonregular path acquisition blocked")
	}
	effects := view.effects.publication()
	if retire {
		if len(effects) != 0 {
			t.Fatal("retired acquisition copied a path")
		}
		return
	}
	if len(effects) != 1 || effects[0].Clipboard == nil || *effects[0].Clipboard != want {
		t.Fatalf("clipboard effect = %+v, want %q", effects, want)
	}
	client := nativeTestPromptClient(u)
	client.event.Frame.Effects = effects
	client.applyEffects()
	if got := client.renderer.terminal.Frame.Clipboard; got != "\x1b]52;c;"+base64.StdEncoding.EncodeToString([]byte(want))+"\x07" {
		t.Fatalf("terminal clipboard = %q", got)
	}
	if view.effects.reserved != 0 {
		t.Fatal("settled path retained effect capacity")
	}
}

func TestNativeNonregularPathCopiesWithoutDialog(t *testing.T) {
	workspace := t.TempDir()
	directory := filepath.Join(workspace, "folder")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(workspace, "linked")
	if err := os.Symlink(directory, link); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"folder", "folder:3-4", (&url.URL{Scheme: "file", Path: directory}).String(), "linked"} {
		t.Run(target, func(t *testing.T) {
			want := directory
			if target == "linked" {
				want = link
			}
			checkNativePathCopy(t, workspace, target, want, false)
		})
	}
	t.Run("retired", func(t *testing.T) { checkNativePathCopy(t, workspace, "folder", directory, true) })
}

func TestMarkdownSymlinkFileAndBrokenTarget(t *testing.T) {
	main, _ := newAppServerTestUI()
	main.attached.cwd = t.TempDir()
	path := filepath.Join(main.attached.cwd, "source.go")
	if err := os.WriteFile(path, []byte("package linked\n"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(main.attached.cwd, "linked.go")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	u := terminalTest(main)
	for _, broken := range []bool{false, true} {
		if broken {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}
		main.detailFiles.openMarkdownFile(&main.attached.session, main.attached.presentation, main.attached.cwd, activitySource(&main.attached, main.shell, main.shell.Transcript), u.Attachment, main.shell.Transcript, "linked.go:1")
		awaitMarkdownFile(t, u)
		if u.Dialog == nil || u.Frame.Clipboard != "" {
			t.Fatal("file symlink did not retain dialog behavior")
		}
		if broken {
			if u.Dialog.Pages[0].Kind != "error" {
				t.Fatal("broken symlink did not show its read error")
			}
		} else if u.Dialog.File.Source != "package linked\n" || u.Dialog.File.First != 1 {
			t.Fatal("regular symlink lost content or line location")
		}
	}
}

func TestNativePathCopyRetainsAdmissionCapacity(t *testing.T) {
	workspace := t.TempDir()
	u, _ := newAppServerTestUI()
	u.startSession("main", workspace)
	_, view, _ := nativeDetailTestViews(t, u)
	reservation := view.effects.reserve(1)
	view.effects.current = reservation
	if refusal := view.openDetails(sessionapi.NativeDetailRequest{Kind: sessionapi.NativeFileDetails, Path: workspace}); refusal != "" {
		t.Fatal(refusal)
	}
	view.effects.current = nil
	reservation.release()
	for range nativeSemanticEffects - 1 {
		view.effects.add(sessionapi.NativeEffect{Clipboard: new("earlier consequence")})
	}
	if view.effects.reserve(1) != nil {
		t.Fatal("pending path copy lost its admission slot")
	}
	select {
	case result := <-view.files.fileReads:
		view.files.loadedMarkdownFile(&view.attached.session, view.attached.presentation, workspace, view.shell, result)
	case <-time.After(5 * time.Second):
		t.Fatal("directory acquisition did not finish")
	}
	if view.effects.reserved != 0 || len(view.effects.pending) != nativeSemanticEffects || *view.effects.pending[nativeSemanticEffects-1].Clipboard != workspace {
		t.Fatal("copy did not settle through its reserved capacity")
	}
}
