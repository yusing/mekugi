package launch

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

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/libraries/commandargs"
	changeview "github.com/yusing/mekugi/modules/changehistory/terminalview"
	"github.com/yusing/mekugi/modules/conversation/composer"

	"github.com/creack/pty"
	terminalui "github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/modules/changehistory/presentation"
	"golang.org/x/term"
)

func nativeEditorTestRun(t *testing.T, c *nativeClient, handoff *nativeEditorHandoff, stdin, stdout *os.File) {
	t.Helper()
	c.completionReady = true
	_, c.completionTarget = c.selectedEditor()
	c.completionInput, c.completionCatalog = c.event.Frame.View.Input.ID, c.event.Frame.CompletionCatalog
	r := &nativeClientRuntime{ctx: t.Context(), active: c}
	if err := c.external(t.Context(), stdin, stdout, handoff, r); err != nil {
		t.Fatal(err)
	}
	c.refresh(time.Now())
}

func TestComposerEditorResultKeepsOriginalTarget(t *testing.T) {
	for _, change := range []string{"publication", "draft", "attachment", "editor"} {
		t.Run(change, func(t *testing.T) {
			u, _ := newAppServerTestUI()
			c := nativeTestPromptClient(u)
			c.ctx = t.Context()
			defer c.renderer.terminal.Diff.Close()
			original, _ := c.selectedEditor()
			original.Replace(composer.Draft{Text: "original"})
			c.refresh(time.Now())
			c.dispatch(terminalsession.Input{Kind: terminalsession.KeyInput, Text: "\x07"})
			handoff := c.handoff
			if handoff == nil || handoff.editor != original {
				t.Fatal("editor shortcut did not retain the original editor")
			}
			switch change {
			case "publication":
				c.event.Frame.View.Version.Revision++
			case "draft":
				original.Replace(composer.Draft{Text: "new draft"})
			case "attachment":
				c.event.Frame.View.Version.Attachment++
			case "editor":
				c.editors = make(map[string]*composer.Editor)
			}
			editor := filepath.Join(t.TempDir(), "editor")
			if err := os.WriteFile(editor, []byte("#!/bin/sh\nprintf 'edited' > \"$1\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("EDITOR", editor)
			nativeEditorTestRun(t, c, handoff, nil, nil)
			if change == "publication" {
				if original.Text != "edited" {
					t.Fatalf("unrelated publication discarded editor result: %q", original.Text)
				}
				return
			}
			_, path, ok := strings.Cut(c.feedback.Text, "draft preserved at ")
			if !ok {
				t.Fatalf("stale editor did not preserve recovery: %q", c.feedback.Text)
			}
			defer os.Remove(path)
			want := "original"
			if change == "draft" {
				want = "new draft"
			}
			if original.Text != want {
				t.Fatalf("stale result changed original: %q", original.Text)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != "edited" {
				t.Fatalf("stale editor recovery = %q, %v", data, err)
			}
		})
	}
}

func TestAppServerComposerEditorDraft(t *testing.T) {
	u, _ := newAppServerTestUI()
	attachImage(&u.attached, u.shell, &u.Editor, &u.questions, &u.approvals, u.clock, &u.ownedImages, u.side, filepath.Join(t.TempDir(), "one.png"))
	appServerTestKeys(t, u, "a")
	attachImage(&u.attached, u.shell, &u.Editor, &u.questions, &u.approvals, u.clock, &u.ownedImages, u.side, filepath.Join(t.TempDir(), "two.png"))
	if err := u.Editor.ApplyExternal("[Image 2]b[Image 1]"); err != nil {
		t.Fatal(err)
	}
	if u.Text != "[Image 1]b[Image 2]" || !strings.HasSuffix(u.Images[0].Path, "two.png") {
		t.Fatalf("reordered images: %q %v", u.Text, u.Images)
	}
	appServerTestKeys(t, u, "\x1a")
	if u.Text != "[Image 1]a[Image 2]" || !strings.HasSuffix(u.Images[0].Path, "one.png") {
		t.Fatalf("editor undo: %q %v", u.Text, u.Images)
	}
	if err := u.Editor.ApplyExternal("[Image 1][Image 1]"); err == nil {
		t.Fatal("duplicate attachment accepted")
	}
	if err := u.Editor.ApplyExternal("cleared"); err != nil {
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
	c := nativeTestPromptClient(u)
	c.ctx = ctx
	c.editor("main").Replace(composer.Draft{Text: "original"})
	c.renderer.terminal.Width, c.renderer.terminal.Height = 120, 30
	c.refresh(time.Now())
	defer c.renderer.terminal.Diff.Close()
	path := filepath.Join(dir, "selected ' $(touch injected).txt")
	if selectedFile {
		if err := os.WriteFile(path, []byte("original file"), 0600); err != nil {
			t.Fatal(err)
		}
		pane := c.renderer.terminal.Auxiliary.(*changeview.Pane)
		pane.SetWorkspace(dir)
		pane.Update((presentation.Publication{}).With([]presentation.File{{Path: path, Chunks: []presentation.Chunk{
			liveDiffCapture("selected", path, 1, "before\n", "original file\n", presentation.Origin{}),
		}}}), "")
		pane.Key("s")
		c.renderer.terminal.Focus, c.renderer.terminal.DiffOpen = 1, true
	}
	done := make(chan error, 1)
	go func() {
		err := terminalui.WithRawPane(ctx, slave, slave, "\x1b[?1049hENTER!", "\x1b[?1049lLEAVE!", func(keys <-chan byte) error {
			if _, err := c.renderer.render(ctx, slave, time.Now(), "test"); err != nil {
				return err
			}
			select {
			case key := <-keys:
				c.dispatch(terminalsession.Input{Kind: terminalsession.KeyInput, Text: string(key)})
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		if err != nil || c.handoff == nil || selectedFile && c.handoff.path != path || !selectedFile && c.handoff.editor == nil {
			done <- fmt.Errorf("editor shortcut: %v", err)
			return
		}
		state, err := term.GetState(int(slave.Fd()))
		if err != nil || !reflect.DeepEqual(state, before) {
			done <- fmt.Errorf("terminal not restored before editor: %v", err)
			return
		}
		c.completionReady = true
		_, c.completionTarget = c.selectedEditor()
		c.completionInput, c.completionCatalog = c.event.Frame.View.Input.ID, c.event.Frame.CompletionCatalog
		if err := c.external(ctx, slave, slave, c.handoff, &nativeClientRuntime{ctx: ctx, active: c}); err != nil {
			done <- err
			return
		}
		if c.notice != "Draft updated from editor" && !selectedFile {
			done <- errors.New(c.notice)
			return
		}
		c.refresh(time.Now())
		done <- terminalui.WithRawPane(ctx, slave, slave, "\x1b[?1049h", "\x1b[?1049lDONE!", func(keys <-chan byte) error {
			var frame bytes.Buffer
			if _, err := c.renderer.render(ctx, io.MultiWriter(slave, &frame), time.Now(), "test"); err != nil {
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
				c.dispatch(terminalsession.Input{Kind: terminalsession.KeyInput, Text: string(key)})
				return nil
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
		if err != nil || string(data) != "edited from terminal" || c.editor("main").Text != "original" || c.renderer.terminal.Focus != 1 || c.renderer.terminal.Auxiliary.(*changeview.Pane).EditorPath() != path {
			t.Fatalf("file=%q draft=%q focus=%d error=%v", data, c.editor("main").Text, c.renderer.terminal.Focus, err)
		}
	} else if c.editor("main").Text != "edited from terminalx" {
		t.Fatalf("draft=%q", c.editor("main").Text)
	}
	after, err := term.GetState(int(slave.Fd()))
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("terminal not restored on exit: %v", err)
	}
}

func TestAppServerComposerEditorFailurePreservesRecovery(t *testing.T) {
	u, _ := newAppServerTestUI()
	c := nativeTestPromptClient(u)
	c.ctx = t.Context()
	c.editor("main").Replace(composer.Draft{Text: "original"})
	c.renderer.terminal.Width, c.renderer.terminal.Height = 120, 30
	c.refresh(time.Now())
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
	if _, err := c.renderer.render(t.Context(), io.Discard, time.Now(), "test"); err != nil {
		t.Fatal(err)
	}
	defer c.renderer.terminal.Diff.Close()
	c.dispatch(terminalsession.Input{Kind: terminalsession.KeyInput, Text: "\x07"})
	if c.handoff == nil {
		t.Fatal("editor shortcut did not request handoff")
	}
	nativeEditorTestRun(t, c, c.handoff, output, output)
	if c.editor("main").Text != "original" {
		t.Fatalf("editor failure changed draft: %q", c.editor("main").Text)
	}
	_, path, ok := strings.Cut(c.feedback.Text, "draft preserved at ")
	if !ok {
		t.Fatalf("missing recovery path: %q", c.feedback.Text)
	}
	defer os.Remove(path)
	var frame bytes.Buffer
	if _, err := c.renderer.render(t.Context(), &frame, time.Now(), "test"); err != nil {
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
