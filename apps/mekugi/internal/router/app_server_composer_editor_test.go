package router

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/libraries/commandargs"
	"github.com/yusing/mekugi/modules/conversation/composer"

	"github.com/creack/pty"
	terminalui "github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/modules/changehistory/presentation"
	"golang.org/x/term"
)

func editorTestIntent(u *appServerUI) sessionpresentation.ComposerIntent {
	v := u.publishPresentation(u.now())
	return sessionpresentation.ComposerIntent{Version: v.Version, Target: v.EditorTarget(), Operation: sessionpresentation.EditExternally}
}

func TestComposerEditorResultKeepsOriginalTarget(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.Editor.Replace(composer.Draft{Text: "original"})
	intent := editorTestIntent(u)
	u.setNotice("unrelated progress", false)
	u.publishPresentation(u.now())
	if err := u.applyEditorDraft(intent, "edited"); err != nil || u.Text != "edited" {
		t.Fatalf("unrelated publication discarded an editor result: %q, %v", u.Text, err)
	}
	intent = editorTestIntent(u)
	u.Editor.Replace(composer.Draft{Text: "new draft"})
	if err := u.applyEditorDraft(intent, "old editor result"); err == nil || u.Text != "new draft" {
		t.Fatalf("returned editor retargeted: %q, %v", u.Text, err)
	}
	questionTestSync(t, u, "replacement-editor", false)
	u.openQuestions()
	if err := u.applyEditorDraft(intent, "old editor result"); err == nil || u.Text != "" {
		t.Fatalf("returned editor reached a prompt: %q, %v", u.Text, err)
	}
}

func TestAppServerComposerEditorDraft(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.attachImage(filepath.Join(t.TempDir(), "one.png"))
	appServerTestKeys(t, u, "a")
	u.attachImage(filepath.Join(t.TempDir(), "two.png"))
	if err := u.applyEditorDraft(editorTestIntent(u), "[Image 2]b[Image 1]"); err != nil {
		t.Fatal(err)
	}
	if u.Text != "[Image 1]b[Image 2]" || !strings.HasSuffix(u.Images[0].Path, "two.png") {
		t.Fatalf("reordered images: %q %v", u.Text, u.Images)
	}
	appServerTestKeys(t, u, "\x1a")
	if u.Text != "[Image 1]a[Image 2]" || !strings.HasSuffix(u.Images[0].Path, "one.png") {
		t.Fatalf("editor undo: %q %v", u.Text, u.Images)
	}
	if err := u.applyEditorDraft(editorTestIntent(u), "[Image 1][Image 1]"); err == nil {
		t.Fatal("duplicate attachment accepted")
	}
	if err := u.applyEditorDraft(editorTestIntent(u), "cleared"); err != nil {
		t.Fatal(err)
	}
	if len(u.Images) != 0 {
		t.Fatal("removed placeholder retained image")
	}
	appServerTestKeys(t, u, "\x1a")
	if len(u.Images) != 2 {
		t.Fatal("undo did not restore attachments")
	}
}

func TestAppServerComposerEditorTerminalHandoff(t *testing.T) {
	testAppServerEditorTerminalHandoff(t, false)
}

func TestAppServerFileEditorTerminalHandoff(t *testing.T) {
	testAppServerEditorTerminalHandoff(t, true)
}

func testAppServerEditorTerminalHandoff(t *testing.T, selectedFile bool) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	before, err := term.GetState(int(slave.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	editor := filepath.Join(dir, "editor with spaces")
	script := "#!/bin/sh\n[ \"$1\" = '--wait' ] || exit 4\nshift\nprintf 'EDITOR!'\nread -r edited\nprintf '%s' \"$edited\" > \"$1\"\n"
	if err := os.WriteFile(editor, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", commandargs.QuoteArgument(editor)+" --wait")
	u, _ := newAppServerTestUI()
	u.ctx = ctx
	u.Text = "original"
	u.ensureShell()
	defer u.shell.diffScreen.Close()
	path := filepath.Join(dir, "selected ' $(touch injected).txt")
	if selectedFile {
		if err := os.WriteFile(path, []byte("original file"), 0600); err != nil {
			t.Fatal(err)
		}
		c := savedPane(u.shell)
		c.SetWorkspace(dir)
		c.Update((presentation.Publication{}).With([]presentation.File{{Path: path, Chunks: []presentation.Chunk{
			liveDiffCapture("selected", path, 1, "before\n", "original file\n", presentation.Origin{}),
		}}}), "")
		c.Key("s")
		u.shell.Focus, u.shell.DiffOpen = 1, true
	}
	done := make(chan error, 1)
	go func() {
		err := terminalui.WithRawPane(ctx, slave, slave, "\x1b[?1049hENTER!", "\x1b[?1049lLEAVE!", func(keys <-chan byte) error {
			if err := u.testPaint(slave, 120, 30); err != nil {
				return err
			}
			select {
			case key := <-keys:
				return u.shell.key(key)
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		var fileEditor *openFileEditor
		var composerEditor *composerEditorRequest
		if selectedFile && !errors.As(err, &fileEditor) || !selectedFile && !errors.As(err, &composerEditor) {
			done <- fmt.Errorf("editor shortcut: %v", err)
			return
		}
		state, err := term.GetState(int(slave.Fd()))
		if err != nil || !reflect.DeepEqual(state, before) {
			done <- fmt.Errorf("terminal not restored before editor: %v", err)
			return
		}
		if selectedFile {
			u.openSelectedFileEditor(fileEditor.path, slave, slave)
		} else {
			u.openComposerEditor(composerEditor.intent, slave, slave)
		}
		if u.feedback.Alert {
			done <- errors.New(u.feedback.Text)
			return
		}
		done <- terminalui.WithRawPane(ctx, slave, slave, "\x1b[?1049h", "\x1b[?1049lDONE!", func(keys <-chan byte) error {
			var frame bytes.Buffer
			if err := u.testPaint(io.MultiWriter(slave, &frame), 120, 30); err != nil {
				return err
			}
			if rows := strings.Count(frame.String(), "\x1b[2K"); rows != 30 {
				return fmt.Errorf("editor return repainted %d of 30 rows", rows)
			}
			if _, err := fmt.Fprint(slave, "RESUME!"); err != nil {
				return err
			}
			select {
			case key := <-keys:
				return u.shell.key(key)
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	// A deadline also prevents a failed handoff from hanging the acceptance test.
	if err := master.SetReadDeadline(time.Now().Add(8 * time.Second)); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(master)
	expect := func(marker string) {
		t.Helper()
		read := make(chan error, 1)
		go func() {
			for {
				s, err := reader.ReadString('!')
				if err != nil || strings.Contains(s, marker) {
					read <- err
					return
				}
			}
		}()
		select {
		case err := <-read:
			if err != nil {
				t.Fatal(err)
			}
		case err := <-done:
			t.Fatalf("pane exited before %s: %v", marker, err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	expect("ENTER!")
	shortcut := byte(7)
	if selectedFile {
		shortcut = 'e'
	}
	if _, err := master.Write([]byte{shortcut}); err != nil {
		t.Fatal(err)
	}
	expect("EDITOR!")
	if _, err := master.Write([]byte("edited from terminal\n")); err != nil {
		t.Fatal(err)
	}
	expect("RESUME!")
	if _, err := master.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if selectedFile {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "edited from terminal" || u.Text != "original" || u.shell.Focus != 1 || savedPane(u.shell).EditorPath() != path {
			t.Fatalf("file=%q draft=%q focus=%d error=%v", data, u.Text, u.shell.Focus, err)
		}
	} else if u.Text != "edited from terminalx" {
		t.Fatalf("draft=%q", u.Text)
	}
	after, err := term.GetState(int(slave.Fd()))
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("terminal not restored on exit: %v", err)
	}
}

func TestAppServerComposerEditorFailurePreservesRecovery(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.Text = "original"
	editor := filepath.Join(t.TempDir(), "editor")
	if err := os.WriteFile(editor, []byte("#!/bin/sh\nprintf 'recover me' > \"$1\"\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", commandargs.QuoteArgument(editor))
	output, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	if err := u.testPaint(io.Discard, 120, 30); err != nil {
		t.Fatal(err)
	}
	defer u.shell.diffScreen.Close()
	u.openComposerEditor(editorTestIntent(u), output, output)
	if u.Text != "original" || !u.feedback.Alert {
		t.Fatalf("editor failure: draft=%q notice=%q", u.Text, u.feedback.Text)
	}
	_, path, ok := strings.Cut(u.feedback.Text, "draft preserved at ")
	if !ok {
		t.Fatalf("missing recovery path: %q", u.feedback.Text)
	}
	defer os.Remove(path)
	var frame bytes.Buffer
	if err := u.testPaint(&frame, 120, 30); err != nil {
		t.Fatal(err)
	}
	if rows := strings.Count(frame.String(), "\x1b[2K"); rows != 30 {
		t.Fatalf("editor failure repainted %d of 30 rows", rows)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "recover me" {
		t.Fatalf("recovery=%q %v", data, err)
	}
}
