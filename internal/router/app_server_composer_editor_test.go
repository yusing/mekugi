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

	"github.com/creack/pty"
	terminalui "github.com/yusing/mekugi/internal/ui/terminal"
	"golang.org/x/term"
)

func TestAppServerComposerEditorDraft(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.attachImage(filepath.Join(t.TempDir(), "one.png"))
	appServerTestKeys(t, u, "a")
	u.attachImage(filepath.Join(t.TempDir(), "two.png"))
	if err := u.applyEditorDraft("[Image 2]b[Image 1]"); err != nil {
		t.Fatal(err)
	}
	if u.draft != "[Image 1]b[Image 2]" || !strings.HasSuffix(u.images[0].path, "two.png") {
		t.Fatalf("reordered images: %q %v", u.draft, u.images)
	}
	appServerTestKeys(t, u, "\x1a")
	if u.draft != "[Image 1]a[Image 2]" || !strings.HasSuffix(u.images[0].path, "one.png") {
		t.Fatalf("editor undo: %q %v", u.draft, u.images)
	}
	if err := u.applyEditorDraft("[Image 1][Image 1]"); err == nil {
		t.Fatal("duplicate attachment accepted")
	}
	if err := u.applyEditorDraft("cleared"); err != nil {
		t.Fatal(err)
	}
	if len(u.images) != 0 {
		t.Fatal("removed placeholder retained image")
	}
	appServerTestKeys(t, u, "\x1a")
	if len(u.images) != 2 {
		t.Fatal("undo did not restore attachments")
	}
}

func TestAppServerComposerEditorTerminalHandoff(t *testing.T) {
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
	t.Setenv("EDITOR", shellQuoteArgument(editor)+" --wait")
	u, _ := newAppServerTestUI()
	u.ctx = ctx
	u.draft = "original"
	u.ensureShell()
	defer u.shell.diffScreen.Close()
	done := make(chan error, 1)
	go func() {
		err := terminalui.WithRawPane(ctx, slave, slave, "\x1b[?1049hENTER!", "\x1b[?1049lLEAVE!", func(keys <-chan byte) error {
			if err := u.paint(slave, 120, 30); err != nil {
				return err
			}
			select {
			case key := <-keys:
				_, err := u.key(key)
				return err
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		if !errors.Is(err, errOpenComposerEditor) {
			done <- fmt.Errorf("editor shortcut: %w", err)
			return
		}
		state, err := term.GetState(int(slave.Fd()))
		if err != nil || !reflect.DeepEqual(state, before) {
			done <- fmt.Errorf("terminal not restored before editor: %v", err)
			return
		}
		u.openComposerEditor(slave, slave)
		if u.noticeAlert {
			done <- errors.New(u.notice)
			return
		}
		done <- terminalui.WithRawPane(ctx, slave, slave, "\x1b[?1049hRESUME!", "\x1b[?1049lDONE!", func(keys <-chan byte) error {
			var frame bytes.Buffer
			if err := u.paint(io.MultiWriter(slave, &frame), 120, 30); err != nil {
				return err
			}
			if rows := strings.Count(frame.String(), "\x1b[2K"); rows != 30 {
				return fmt.Errorf("editor return repainted %d of 30 rows", rows)
			}
			select {
			case key := <-keys:
				_, err := u.key(key)
				return err
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
		for {
			s, err := reader.ReadString('!')
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(s, marker) {
				return
			}
		}
	}
	expect("ENTER!")
	if _, err := master.Write([]byte{7}); err != nil {
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
	if u.draft != "edited from terminalx" {
		t.Fatalf("draft=%q", u.draft)
	}
	after, err := term.GetState(int(slave.Fd()))
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("terminal not restored on exit: %v", err)
	}
}

func TestAppServerComposerEditorFailurePreservesRecovery(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.draft = "original"
	editor := filepath.Join(t.TempDir(), "editor")
	if err := os.WriteFile(editor, []byte("#!/bin/sh\nprintf 'recover me' > \"$1\"\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", shellQuoteArgument(editor))
	output, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	if err := u.paint(io.Discard, 120, 30); err != nil {
		t.Fatal(err)
	}
	defer u.shell.diffScreen.Close()
	u.openComposerEditor(output, output)
	if u.draft != "original" || !u.noticeAlert {
		t.Fatalf("editor failure: draft=%q notice=%q", u.draft, u.notice)
	}
	_, path, ok := strings.Cut(u.notice, "draft preserved at ")
	if !ok {
		t.Fatalf("missing recovery path: %q", u.notice)
	}
	defer os.Remove(path)
	var frame bytes.Buffer
	if err := u.paint(&frame, 120, 30); err != nil {
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
