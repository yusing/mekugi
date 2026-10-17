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
			main.session.cwd = t.TempDir()
			main.view.Painter.Theme = diffrender.TerminalTheme
			path := filepath.Join(main.session.cwd, "sample."+extension)
			content := strings.Repeat("// preceding line\n", 39) + "var target = 1\n"
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			u := &terminalUI{main: main, width: 80, height: 18}
			u.openMarkdownFile(main.view, "sample."+extension+":40")
			rows := make([]string, u.height)
			u.paintOutput(rows, u.width, u.height)
			if extension == "go" {
				uisnapshot.AssertTerminal(t, "testdata/snapshots/native-markdown-file-loading.txt", rows, u.width)
			}
			x, y := outputSelectionPoint(t, u, "Loading")
			outputSelectionDrag(u, x, y, x+6, y)
			selected := u.output.Selection.Text()
			awaitMarkdownFile(t, u)
			drawOutputDialog(u) // Completion repaints while the loading selection remains frozen.
			if u.output.Selection == nil || u.output.Selection.Text() != selected {
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
			if u.frame.Clipboard != "\x1b]52;c;"+base64.StdEncoding.EncodeToString([]byte(content))+"\x07" {
				t.Fatal("source copy reread the changed file")
			}
			u.openMarkdownFile(main.view, "sample."+extension)
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
			main.ensureShell()
			main.session.cwd = t.TempDir()
			path := filepath.Join(main.session.cwd, "source.go")
			if err := os.WriteFile(path, []byte("package stale\n"), 0600); err != nil {
				t.Fatal(err)
			}
			u := main.shell
			u.width, u.height = 80, 18
			u.openMarkdownFile(main.view, path)
			results := u.markdownFileUpdates()
			switch boundary {
			case "close":
				u.outputKey("q")
			case "replace":
				u.openBlocks(main.view, []activityui.Block{{Body: "replacement dialog"}})
			case "thread":
				main.testActivity(main.agents).displayThread("/root/child")
			case "retirement":
				main.session.Retire()
			case "attachment":
				if err := main.clearSessionPresentation(); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case result := <-results:
				u.loadedMarkdownFile(result)
			case <-time.After(5 * time.Second):
				t.Fatal("file read did not finish")
			}
			if boundary == "replace" {
				if frame := drawOutputDialog(u); !strings.Contains(frame, "replacement dialog") || strings.Contains(frame, "package stale") {
					t.Fatalf("stale completion changed replacement: %s", frame)
				}
			} else if u.output != nil {
				t.Fatal("stale completion retained a retired dialog")
			}
		})
	}
}

func TestMarkdownFileCapacityAndReadErrors(t *testing.T) {
	main, _ := newAppServerTestUI()
	main.session.cwd = t.TempDir()
	u := &terminalUI{main: main, width: 80, height: 18, fileReadSlot: make(chan struct{}, 1)}
	// Reserve physical capacity independently of the dialog. This models an
	// outstanding syscall, not a kernel-blocked filesystem fixture.
	u.fileReadSlot <- struct{}{}
	u.openMarkdownFile(main.view, "missing.go")
	if frame := drawOutputDialog(u); !strings.Contains(frame, "capacity exhausted") {
		t.Fatalf("exhausted read capacity was hidden: %s", frame)
	}
	u.outputKey("q")
	if len(u.fileReadSlot) != 1 {
		t.Fatal("closing a dialog released physical read capacity")
	}
	<-u.fileReadSlot
	for _, name := range []string{"oversized.go", "unreadable.go"} {
		path := filepath.Join(main.session.cwd, name)
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
		u.openMarkdownFile(main.view, name)
		awaitMarkdownFile(t, u)
		drawOutputDialog(u)
		if u.output.pages[0].Kind != "error" {
			if name == "unreadable.go" && os.Geteuid() == 0 {
				continue // Root bypasses this fixture's mode-bit denial.
			}
			t.Fatalf("%s was not rejected", name)
		}
		text := u.output.Document.Text
		u.outputKey("y")
		if text == "" || u.frame.Clipboard != "\x1b]52;c;"+base64.StdEncoding.EncodeToString([]byte(text))+"\x07" {
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
	p.ui.session.cwd = t.TempDir()
	path := filepath.Join(p.ui.session.cwd, "source.go")
	if err := os.WriteFile(path, []byte("package replay_file\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p.ui.shell.openMarkdownFile(p.ui.view, "source.go")
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
