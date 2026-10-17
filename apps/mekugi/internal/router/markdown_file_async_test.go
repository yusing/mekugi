package router

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffrender"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestUISnapshotMarkdownFileLoading(t *testing.T) {
	for _, extension := range []string{"go", "md"} {
		t.Run(extension, func(t *testing.T) {
			main, _ := newAppServerTestUI()
			main.cwd = t.TempDir()
			main.shell.Transcript.Painter.Theme = diffrender.TerminalTheme
			path := filepath.Join(main.cwd, "sample."+extension)
			content := strings.Repeat("// preceding line\n", 39) + "var target = 1\n"
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			u := terminalTest(main)
			u.Width, u.Height = 80, 18
			u.main.detailFiles.openMarkdownFile(&u.main.session, u.main.sessionPresentation(), u.main.cwd, u.main.activitySource(main.shell.Transcript), u.Attachment, main.shell.Transcript, "sample."+extension+":40")
			rows := make([]string, u.Height)
			u.main.detailFiles.prepareOutput(&u.main.session, u.Attachment)
			u.Dialog.Paint(rows, u.Width, u.Height)
			if extension == "go" {
				uisnapshot.AssertTerminal(t, "testdata/snapshots/native-markdown-file-loading.txt", rows, u.Width)
			}
			x, y := outputSelectionPoint(t, u, "Loading")
			outputSelectionDrag(u, x, y, x+6, y)
			selected := u.Dialog.Selection.Text()
			awaitMarkdownFile(t, u)
			drawOutputDialog(u) // Completion repaints while the loading selection remains frozen.
			if u.Dialog.Selection == nil || u.Dialog.Selection.Text() != selected {
				t.Fatal("completion replaced the frozen loading selection")
			}
			u.outputKey("\x1b")
			frame := drawOutputDialog(u)
			if extension == "md" {
				u.outputKey("\x1b[C") // The source tab reveals the line after rendered Markdown.
				frame = drawOutputDialog(u)
			}
			if strings.Contains(frame, "Loading") || !strings.Contains(frame, "var target = 1") {
				t.Fatalf("completion did not invalidate loading layout and reveal line 40: %s", frame)
			}
			if err := os.WriteFile(path, []byte("changed after opening\n"), 0600); err != nil {
				t.Fatal(err)
			}
			u.outputKey("y")
			if u.Frame.Clipboard != "\x1b]52;c;"+base64.StdEncoding.EncodeToString([]byte(content))+"\x07" {
				t.Fatal("source copy reread the changed file")
			}
			u.main.detailFiles.openMarkdownFile(&u.main.session, u.main.sessionPresentation(), u.main.cwd, u.main.activitySource(main.shell.Transcript), u.Attachment, main.shell.Transcript, "sample."+extension)
			awaitMarkdownFile(t, u)
			if frame := drawOutputDialog(u); !strings.Contains(frame, "changed after opening") {
				t.Fatalf("reopening did not read current content: %s", frame)
			}
		})
	}
}

func TestMarkdownFileLateCompletion(t *testing.T) {
	for _, boundary := range []string{"close", "replace", "thread", "attachment", "retirement"} {
		t.Run(boundary, func(t *testing.T) {
			main, _ := newAppServerTestUI()

			main.cwd = t.TempDir()
			path := filepath.Join(main.cwd, "source.go")
			if err := os.WriteFile(path, []byte("package stale\n"), 0600); err != nil {
				t.Fatal(err)
			}
			u := terminalTest(main)
			u.Width, u.Height = 80, 18
			u.main.detailFiles.openMarkdownFile(&u.main.session, u.main.sessionPresentation(), u.main.cwd, u.main.activitySource(main.shell.Transcript), u.Attachment, main.shell.Transcript, path)
			results := u.main.fileReads
			switch boundary {
			case "close":
				u.outputKey("q")
			case "replace":
				u.openBlocks(main.shell.Transcript, []activityui.Block{{Body: "replacement dialog"}})
			case "thread":
				main.testActivity(main.shell.Agents).displayThread("/root/child")
			case "retirement":
				main.session.Retire()
			case "attachment":
				if err := main.clearSessionPresentation(); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case result := <-results:
				u.main.detailFiles.loadedMarkdownFile(&u.main.session, u.main.presentationState, u.main.cwd, u.Attachment, result)
			case <-time.After(5 * time.Second):
				t.Fatal("file read did not finish")
			}
			if boundary == "replace" {
				if frame := drawOutputDialog(u); !strings.Contains(frame, "replacement dialog") || strings.Contains(frame, "package stale") {
					t.Fatalf("stale completion changed replacement: %s", frame)
				}
			} else if u.Dialog != nil {
				t.Fatal("stale completion retained a retired dialog")
			}
		})
	}
}

func TestMarkdownFileCapacityAndReadErrors(t *testing.T) {
	main, _ := newAppServerTestUI()
	main.cwd = t.TempDir()
	main.fileReadSlot = make(chan struct{}, 1)
	u := terminalTest(main)
	u.Width, u.Height = 80, 18
	// Reserve physical capacity independently of the dialog. This models an
	// outstanding syscall, not a kernel-blocked filesystem fixture.
	main.fileReadSlot <- struct{}{}
	u.main.detailFiles.openMarkdownFile(&u.main.session, u.main.sessionPresentation(), u.main.cwd, u.main.activitySource(main.shell.Transcript), u.Attachment, main.shell.Transcript, "missing.go")
	if frame := drawOutputDialog(u); !strings.Contains(frame, "capacity exhausted") {
		t.Fatalf("exhausted read capacity was hidden: %s", frame)
	}
	u.outputKey("q")
	if len(main.fileReadSlot) != 1 {
		t.Fatal("closing a dialog released physical read capacity")
	}
	<-main.fileReadSlot
	for _, name := range []string{"oversized.go", "unreadable.go"} {
		path := filepath.Join(main.cwd, name)
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
		if name == "oversized.go" {
			if err := os.Truncate(path, (8<<20)+1); err != nil {
				t.Fatal(err)
			}
		} else if err := os.Chmod(path, 0); err != nil {
			t.Fatal(err)
		}
		u.main.detailFiles.openMarkdownFile(&u.main.session, u.main.sessionPresentation(), u.main.cwd, u.main.activitySource(main.shell.Transcript), u.Attachment, main.shell.Transcript, name)
		awaitMarkdownFile(t, u)
		drawOutputDialog(u)
		if u.Dialog.Pages[0].Kind != "error" {
			if name == "unreadable.go" && os.Geteuid() == 0 {
				continue // Root bypasses this fixture's mode-bit denial.
			}
			t.Fatalf("%s was not rejected", name)
		}
		text := u.Dialog.Document.Text
		u.outputKey("y")
		if text == "" || u.Frame.Clipboard != "\x1b]52;c;"+base64.StdEncoding.EncodeToString([]byte(text))+"\x07" {
			t.Fatalf("%s error was not copyable", name)
		}
	}
}

func TestSessionUIReplayPausedMarkdownFileCompletion(t *testing.T) {
	p := replayPlaybackTestNew(t)
	p.Paused = true
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	p.ctx = ctx
	p.ui.cwd = t.TempDir()
	p.publish()
	path := filepath.Join(p.ui.cwd, "source.go")
	if err := os.WriteFile(path, []byte("package replay_file\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p.ui.detailFiles.openMarkdownFile(&p.ui.session, p.ui.presentation, p.ui.cwd, p.ui.source, p.ui.shell, p.ui.view, "source.go")
	frames := 0
	err := p.run(make(chan byte), func() error {
		var frame strings.Builder
		if err := p.paint(&frame, 100, 24); err != nil {
			return err
		}
		frames++
		if strings.Contains(ansi.Strip(frame.String()), "package replay_file") {
			cancel()
		}
		return nil
	}, false, io.Discard)
	if !errors.Is(err, context.Canceled) || frames != 2 {
		t.Fatalf("paused replay needed further input to paint completion: frames=%d err=%v", frames, err)
	}
}
