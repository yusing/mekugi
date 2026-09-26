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
				t.Fatalf("paste = %q, images=%v, status=%s", u.draft, u.images, u.status)
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
			if !u.alert || !strings.Contains(u.status, "PNG image") || len(u.images) != 0 {
				t.Fatalf("bad clipboard not reported: %q", u.status)
			}
		})
	}
}
