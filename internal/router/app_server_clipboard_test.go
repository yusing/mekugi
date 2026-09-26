package router

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

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
			appServerTestKeys(t, u, "before \x16 after")
			if u.draft != "before [Image 1] after" || len(u.images) != 1 {
				t.Fatalf("paste = %q, images=%v, notice=%s", u.draft, u.images, u.notice)
			}
			path := u.images[0].path
			t.Cleanup(func() { _ = os.Remove(path) })
			data, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(data, pngData.Bytes()) {
				t.Fatalf("retained image differs: %v", err)
			}
			// An undoable deletion retains the image until its draft history is released.
			appServerTestKeys(t, u, "\x1b[H\x1b[C\x1b[C\x1b[C\x1b[C\x1b[C\x1b[C\x1b[C\x1b[3~")
			if u.draft != "before  after" || len(u.images) != 0 {
				t.Fatalf("delete image = %q, %v", u.draft, u.images)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("undoable image lost: %v", err)
			}
			u.undoDrafts, u.redoDrafts = nil, nil
			u.pruneDraftImages()
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("unreferenced draft image remains: %v", err)
			}
			if err := os.WriteFile(source, []byte("not an image"), 0600); err != nil {
				t.Fatal(err)
			}
			appServerTestKeys(t, u, "\x16")
			if !u.noticeAlert || !strings.Contains(u.notice, "PNG image") || len(u.images) != 0 {
				t.Fatalf("bad clipboard not reported: %q", u.notice)
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
		appServerTestKeys(t, u, "see \x1b[200~"+pasted+"\x1b[201~now")
		if u.draft != "see [Image 1] now" || len(u.images) != 1 || u.images[0].path != path {
			t.Fatalf("paste %q = %q, images=%v", pasted, u.draft, u.images)
		}
		appServerTestKeys(t, u, "\x1a\x1a")
		if u.draft != "see " {
			t.Fatalf("attachment and its space are not one undo: %q", u.draft)
		}
		u.undoDrafts, u.redoDrafts = nil, nil
		u.pruneDraftImages()
		u.discardDraftImages()
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("pasted user file removed: %v", err)
		}
	}
	for _, pasted := range []string{text, path + " " + path, "relative.png", filepath.Join(dir, "missing.png"), dir, "file://host" + path} {
		u, _ := newAppServerTestUI()
		appServerTestKeys(t, u, "\x1b[200~"+pasted+"\x1b[201~")
		if u.draft != pasted || len(u.images) != 0 {
			t.Fatalf("non-image paste %q = %q, images=%v", pasted, u.draft, u.images)
		}
	}
}
