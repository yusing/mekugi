package claude

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/session"
)

func TestClaudeNativeModelSwitchDuringStreaming(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_NATIVE_CLAUDE") != "1" {
		t.Skip("set MEKUGI_TEST_NATIVE_CLAUDE=1 for two real native model-control prompts")
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
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	c, err := Start(ctx, node, bridge, Config{Cwd: t.TempDir(), Executable: executable, Model: "haiku"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	next := func() session.Event {
		select {
		case e, ok := <-c.Events():
			if !ok || e.Kind == "error" || e.Kind == "done" && e.Failed {
				t.Fatalf("native model control failed: %s", e.Text)
			}
			if e.Kind == "tool" || e.Kind == "prompt" {
				t.Fatal("native settings fixture unexpectedly invoked tools")
			}
			return e
		case <-ctx.Done():
			t.Fatal(ctx.Err())
			return session.Event{}
		}
	}
	effortAvailable := false
	for {
		e := next()
		if e.Kind == "ready" {
			sonnet := false
			for _, model := range e.Models {
				sonnet = sonnet || model.ID == "sonnet"
				if model.ID == "sonnet" && model.SupportsEffort && slices.Contains(model.Efforts, "low") {
					effortAvailable = true
				}
			}
			if !sonnet {
				t.Fatal("installed compatibility target does not advertise sonnet")
			}
			break
		}
	}
	if err := c.Send(ctx, "Count from 1 through 100, one number per line. Do not use tools."); err != nil {
		t.Fatal(err)
	}
	sent, receipt, done := false, false, false
	effortReceipt := !effortAvailable
	for !done || !receipt || !effortReceipt {
		e := next()
		if e.Kind == "message" && e.Text != "" && !sent {
			if done {
				t.Fatal("no visible text before native turn completion")
			}
			if err := c.SetSettings(ctx, session.Settings{ID: "native-stream-switch", Field: "model", Value: "sonnet"}); err != nil {
				t.Fatal(err)
			}
			sent = true
			if effortAvailable {
				if err := c.SetSettings(ctx, session.Settings{ID: "native-stream-effort", Field: "effort", Value: "low"}); err != nil {
					t.Fatal(err)
				}
			}
		}
		if e.Kind == "settings" && e.Settings != nil && e.Settings.ID == "native-stream-switch" {
			if e.Failed || e.Settings.Field != "model" || e.Settings.Value != "sonnet" {
				t.Fatalf("native switch rejected or miscorrelated: %s", e.Text)
			}
			receipt = true
		}
		if e.Kind == "settings" && e.Settings != nil && e.Settings.ID == "native-stream-effort" {
			if e.Failed || e.Settings.Field != "effort" || e.Settings.Value != "low" {
				t.Fatalf("native effort request rejected or miscorrelated: %s", e.Text)
			}
			effortReceipt = true
		}
		if e.Kind == "done" {
			done = true
		}
	}
	if !sent {
		t.Fatal("model setter was not issued while streaming")
	}
	if err := c.Send(ctx, "Reply only NATIVE_SWITCH_OK. Do not use tools."); err != nil {
		t.Fatal(err)
	}
	answer, observed := false, false
	for !observed {
		e := next()
		if e.Kind == "message" && strings.TrimSpace(e.Text) == "NATIVE_SWITCH_OK" {
			answer = true
		}
		if e.Kind == "usage" && answer {
			for model, usage := range e.Usage.Models {
				if strings.Contains(strings.ToLower(model), "sonnet") && usage.Output != nil && *usage.Output > 0 {
					observed = true
				}
			}
			if !observed {
				t.Fatal("native provider usage did not establish subsequent Sonnet output")
			}
		}
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	t.Log("Native model control issued during public text streaming, acknowledged, and subsequent Sonnet output reported")
	if effortAvailable {
		t.Log("Advertised native low-effort request acknowledged; effective policy-limited effort is not inferred")
	}
}
