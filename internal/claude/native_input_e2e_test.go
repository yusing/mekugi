package claude

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/session"
)

// One explicit native prompt validates SDK image delivery and stock mention
// handling. No tool, instruction, plugin or permission mode is injected.
func TestClaudeNativeImageAndFileMention(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_NATIVE_CLAUDE") != "1" {
		t.Skip("set MEKUGI_TEST_NATIVE_CLAUDE=1 for one real native attachment prompt")
	}
	executable, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := filepath.Abs("bridge/dist/bridge.js")
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "marker.txt"), []byte("NATIVE_ATTACHMENT_MARKER"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workspace, "green.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	im := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := range 64 {
		for x := range 64 {
			im.SetRGBA(x, y, color.RGBA{G: 255, A: 255})
		}
	}
	err = png.Encode(f, im)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("image: %v, close: %v", err, closeErr)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	c, err := Start(ctx, node, bridge, Config{Cwd: workspace, Executable: executable, Model: "haiku"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	next := func() session.Event {
		select {
		case e, ok := <-c.Events():
			if !ok {
				t.Fatal("native bridge disconnected")
			}
			if e.Kind == "error" || e.Kind == "done" && e.Failed {
				t.Fatalf("native runtime: %s", e.Text)
			}
			return e
		case <-ctx.Done():
			t.Fatal(ctx.Err())
			return session.Event{}
		}
	}
	for e := next(); e.Kind != "ready"; e = next() {
	}
	if err := c.SendInput(ctx, []session.InputPart{
		{Text: "@marker.txt\nReply with only the image's dominant color in uppercase and the exact marker in the mentioned file. You may use native Read if needed to read the file. Do not use other tools."},
		{ImagePath: path},
	}); err != nil {
		t.Fatal(err)
	}
	answer := ""
	for {
		e := next()
		if e.Kind == "tool" && e.Role != "Read" {
			t.Fatal("native attachment prompt used an unexpected tool")
		}
		if e.Kind == "prompt" {
			if e.Prompt == nil || e.Prompt.Tool != "Read" {
				t.Fatal("native attachment prompt asked an unexpected permission")
			}
			if err := c.Respond(ctx, session.Decision{ID: e.Prompt.ID, Allow: true}); err != nil {
				t.Fatal(err)
			}
		}
		if e.Kind == "message" {
			answer = e.Text
		}
		if e.Kind == "done" {
			break
		}
	}
	if (!strings.Contains(answer, "GREEN") && !strings.Contains(answer, "LIME")) || !strings.Contains(answer, "NATIVE_ATTACHMENT_MARKER") {
		t.Fatalf("native image or @path handling missing: %q", answer)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	t.Log("Native image recognized and unchanged @path resolved through Claude's workflow")
}
