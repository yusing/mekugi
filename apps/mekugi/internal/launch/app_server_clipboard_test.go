package launch

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
)

// nativeTestInput decodes bytes through the native client's actual input owner.
func nativeTestInput(c *nativeClient, text string) {
	for i := range len(text) {
		if event := c.renderer.terminal.Decoder.Feed(text[i], time.Now()); event.Kind != terminalsession.NoInput {
			c.dispatch(event)
			c.refresh(time.Now())
		}
	}
}

func nativeTestClipboard(t *testing.T, c *nativeClient) {
	t.Helper()
	nativeTestInput(c, "\x16")
	select {
	case message := <-c.runtime.messages:
		if message.owner != c || message.clipboard == nil {
			t.Fatal("clipboard result lost its owner")
		}
		if err := c.runtime.deliver(message); err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("clipboard acquisition did not finish")
	}
	c.workers.Wait()
}

func nativeTestClipboardClient(t *testing.T, u *appServerUI) *nativeClient {
	t.Helper()
	c := nativeTestPromptClient(u)
	c.ctx, c.stop = context.WithCancel(t.Context())
	c.ownedImages = make(map[string]bool)
	c.runtime = &nativeClientRuntime{active: c, messages: make(chan nativeClientMessage), images: c.ownedImages}
	t.Cleanup(func() { c.stop(); c.workers.Wait(); c.runtime.close(); c.renderer.terminal.Diff.Close() })
	return c
}

func TestAppServerClipboardImage(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux clipboard command integration")
	}
	for _, backend := range []string{"wayland", "x11"} {
		t.Run(backend, func(t *testing.T) {
			dir := t.TempDir()
			var pngData bytes.Buffer
			if err := png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(dir, "clipboard.png")
			if err := os.WriteFile(source, pngData.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("COMPOSER_TEST_PNG", source)
			name := "xclip"
			t.Setenv("WAYLAND_DISPLAY", "")
			expected := "-selection clipboard -t image/png -o"
			if backend == "wayland" {
				name = "wl-paste"
				t.Setenv("WAYLAND_DISPLAY", "test-display")
				expected = "--no-newline --type image/png"
			}
			script := "#!/bin/sh\n[ \"$*\" = \"" + expected + "\" ] || exit 4\n/bin/cat \"$COMPOSER_TEST_PNG\"\n"
			if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir)
			u, _ := newAppServerTestUI()
			c := nativeTestClipboardClient(t, u)
			nativeTestInput(c, "before ")
			nativeTestClipboard(t, c)
			nativeTestInput(c, " after")
			editor := c.editor("main")
			if editor.Text != "before [Image 1] after" || len(editor.Images) != 1 {
				t.Fatalf("paste = %q, images=%v, notice=%s", editor.Text, editor.Images, c.notice)
			}
			path := editor.Images[0].Path
			t.Cleanup(func() { _ = os.Remove(path) })
			data, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(data, pngData.Bytes()) {
				t.Fatalf("retained image differs: %v", err)
			}
			// Deletion retains the client-owned image for undo and session lifetime.
			nativeTestInput(c, "\x1b[H\x1b[C\x1b[C\x1b[C\x1b[C\x1b[C\x1b[C\x1b[C\x1b[3~")
			if editor.Text != "before  after" || len(editor.Images) != 0 {
				t.Fatalf("delete image = %q, %v", editor.Text, editor.Images)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("undoable image lost: %v", err)
			}
			editor.UndoDrafts, editor.RedoDrafts = nil, nil
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("client-owned image lost before session close: %v", err)
			}
			if err := os.WriteFile(source, []byte("not an image"), 0600); err != nil {
				t.Fatal(err)
			}
			nativeTestClipboard(t, c)
			if !strings.Contains(c.feedback.Text, "PNG image") || len(editor.Images) != 0 {
				t.Fatalf("bad clipboard not reported: %q", c.feedback.Text)
			}
			c.runtime.close()
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("session-close image cleanup: %v", err)
			}
		})
	}
}

func TestAppServerPastedImagePathAttaches(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "My Shots")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "shot it's.png")
	if err := os.WriteFile(path, pngData.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	text := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(text, []byte("not an image"), 0600); err != nil {
		t.Fatal(err)
	}
	escaped := strings.NewReplacer(" ", `\ `, "'", `\'`).Replace(path)
	for _, pasted := range []string{
		path,
		escaped + "\r",
		`"` + path + `"`,
		"'" + strings.ReplaceAll(path, "'", `'\''`) + "'",
		"file://" + strings.NewReplacer(" ", "%20", "'", "%27").Replace(path),
	} {
		u, _ := newAppServerTestUI()
		c := nativeTestClipboardClient(t, u)
		editor := c.editor("main")
		nativeTestInput(c, "see \x1b[200~"+pasted+"\x1b[201~now")
		if editor.Text != "see [Image 1] now" || len(editor.Images) != 1 || editor.Images[0].Path != path {
			t.Fatalf("paste %q = %q, images=%v", pasted, editor.Text, editor.Images)
		}
		nativeTestInput(c, "\x1a\x1a")
		if editor.Text != "see " {
			t.Fatalf("attachment and its space are not one undo: %q", editor.Text)
		}
		c.runtime.close()
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("pasted user file removed: %v", err)
		}
	}
	for _, pasted := range []string{text, path + " " + path, "relative.png", filepath.Join(dir, "missing.png"), dir, "file://host" + path} {
		u, _ := newAppServerTestUI()
		c := nativeTestClipboardClient(t, u)
		editor := c.editor("main")
		nativeTestInput(c, "\x1b[200~"+pasted+"\x1b[201~")
		if editor.Text != pasted || len(editor.Images) != 0 {
			t.Fatalf("non-image paste %q = %q, images=%v", pasted, editor.Text, editor.Images)
		}
	}
}
