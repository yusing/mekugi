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

func TestClaudeNativeForkKeepsParentConversation(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_NATIVE_CLAUDE") != "1" {
		t.Skip("set MEKUGI_TEST_NATIVE_CLAUDE=1 for four real native fork/resume prompts")
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
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	workspace := t.TempDir()
	start := func(resume string, fork bool) *Client {
		c, err := Start(ctx, node, bridge, Config{Cwd: workspace, Executable: executable, Resume: resume, ForkSession: fork, Model: "haiku"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		return c
	}
	next := func(c *Client) session.Event {
		select {
		case e, ok := <-c.Events():
			if !ok || e.Kind == "error" || e.Kind == "done" && e.Failed {
				t.Fatalf("native fork/resume failed: %s", e.Text)
			}
			if e.Kind == "tool" || e.Kind == "prompt" {
				t.Fatal("native fork fixture unexpectedly invoked tools")
			}
			return e
		case <-ctx.Done():
			t.Fatal(ctx.Err())
			return session.Event{}
		}
	}
	ready := func(c *Client, requireHistory bool) {
		history := false
		for {
			e := next(c)
			if e.Historical && e.Kind == "message" && strings.Contains(e.Text, "ROOT_TOKEN_731") {
				history = true
			}
			if e.Kind == "ready" {
				if requireHistory && !history {
					t.Fatal("native parent display history missing")
				}
				return
			}
		}
	}
	turn := func(c *Client, text string) (id, answer string) {
		if err := c.Send(ctx, text); err != nil {
			t.Fatal(err)
		}
		for {
			e := next(c)
			if e.Kind == "session" {
				id = e.SessionID
			}
			if e.Kind == "message" {
				answer = e.Text
			}
			if e.Kind == "done" {
				if id == "" {
					t.Fatal("native init identity missing")
				}
				if err := c.Close(); err != nil {
					t.Fatal(err)
				}
				return
			}
		}
	}
	c := start("", false)
	ready(c, false)
	parent, answer := turn(c, "Our only marker is ROOT_TOKEN_731. Reply only the exact marker. Do not use tools.")
	if strings.TrimSpace(answer) != "ROOT_TOKEN_731" {
		t.Fatalf("root marker missing: %q", answer)
	}
	c = start(parent, true)
	ready(c, true)
	fork, answer := turn(c, "New additional marker FORK_TOKEN_582. Reply with the original marker followed by this new marker. Do not use tools.")
	if fork == parent || !strings.Contains(answer, "ROOT_TOKEN_731") || !strings.Contains(answer, "FORK_TOKEN_582") {
		t.Fatalf("fork identity/context not independent: distinct=%t answer=%q", fork != parent, answer)
	}
	c = start(parent, false)
	ready(c, true)
	resumed, answer := turn(c, "Reply with all previously introduced markers, space-separated. Do not use tools.")
	if resumed != parent || strings.TrimSpace(answer) != "ROOT_TOKEN_731" {
		t.Fatalf("fork leaked into parent: same=%t answer=%q", resumed == parent, answer)
	}
	c = start(fork, false)
	ready(c, true)
	resumed, answer = turn(c, "Reply with all previously introduced markers, space-separated. Do not use tools.")
	if resumed != fork || !strings.Contains(answer, "ROOT_TOKEN_731") || !strings.Contains(answer, "FORK_TOKEN_582") {
		t.Fatalf("fresh fork resume lost identity/context: same=%t answer=%q", resumed == fork, answer)
	}
	t.Log("Native fork received new identity/context; fresh parent and fork resumes preserved independent conversations")
}
