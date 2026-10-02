package claude

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/session"
)

// Explicit opt-in: two real user prompts, native authentication/billing/config.
// This establishes SDK transport/usage/resume, not terminal UX or policy eligibility.
func TestClaudeNativeUsageAndFreshBridgeResume(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_NATIVE_CLAUDE") != "1" {
		t.Skip("set MEKUGI_TEST_NATIVE_CLAUDE=1 for two real native Claude prompts")
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
	if _, err := os.Stat(bridge); err != nil {
		t.Fatal("build the pinned bridge before live acceptance")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	workspace := t.TempDir() // A normal fixture, never a Git worktree.
	start := func(resume string) *Client {
		c, err := Start(ctx, node, bridge, Config{Cwd: workspace, Executable: executable, Resume: resume, Model: "haiku"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		return c
	}
	next := func(c *Client) session.Event {
		select {
		case e, ok := <-c.Events():
			if !ok {
				t.Fatal("native bridge disconnected")
			}
			if e.Kind == "error" || e.Kind == "done" && e.Failed {
				t.Fatalf("native runtime failed: %s", e.Text)
			}
			return e
		case <-ctx.Done():
			t.Fatal(ctx.Err())
			return session.Event{}
		}
	}
	c := start("")
	for e := next(c); e.Kind != "ready"; e = next(c) {
	}
	if err := c.Send(ctx, "Reply with only NATIVE_USAGE_OK. Do not use tools."); err != nil {
		t.Fatal(err)
	}
	id, answer := "", false
	var first *session.Usage
	for first == nil {
		e := next(c)
		if e.Kind == "session" {
			id = e.SessionID
		}
		if e.Kind == "message" && strings.TrimSpace(e.Text) == "NATIVE_USAGE_OK" {
			answer = true
		}
		if e.Kind == "usage" {
			first = e.Usage
		}
		if e.Kind == "tool" {
			t.Fatal("native runtime unexpectedly used a tool")
		}
	}
	if id == "" || !answer || len(first.Models) == 0 {
		t.Fatal("native identity, answer or reported usage missing")
	}
	var output uint64
	for _, m := range first.Models {
		if m.Output != nil {
			output += *m.Output
		}
	}
	if output == 0 {
		t.Fatal("successful native answer supplied no output-token evidence")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	c = start(id)
	history := false
	for {
		e := next(c)
		if e.Historical && e.Kind == "message" && strings.TrimSpace(e.Text) == "NATIVE_USAGE_OK" {
			history = true
		}
		if e.Kind == "ready" {
			break
		}
	}
	if !history {
		t.Fatal("fresh bridge did not restore native transcript display")
	}
	if err := c.Send(ctx, "What was the exact marker you just replied with? Reply only the marker. Do not use tools."); err != nil {
		t.Fatal(err)
	}
	answer = false
	for {
		e := next(c)
		if e.Kind == "session" && e.SessionID != id {
			t.Fatal("resume substituted another session")
		}
		if e.Kind == "message" && strings.TrimSpace(e.Text) == "NATIVE_USAGE_OK" {
			answer = true
		}
		if e.Kind == "tool" {
			t.Fatal("resume unexpectedly replayed or invoked a tool")
		}
		if e.Kind == "usage" {
			var resumed uint64
			for _, m := range e.Usage.Models {
				if m.Output != nil {
					resumed += *m.Output
				}
			}
			if !answer || resumed <= output {
				t.Fatal("native context or retained cumulative usage was not resumed")
			}
			break
		}
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	t.Log("Two native answers, reported cumulative usage, fresh SDK bridge resume and historical display passed; no tools invoked")
}
