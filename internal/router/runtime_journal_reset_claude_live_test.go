package router

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/claude"
)

// Four real prompts: slice one, fresh-context automatic continuation, fresh
// bridge resume, and a reset of that resumed bridge. Native policy/billing stay.
func TestRuntimeJournalResetClaudeLive(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_NATIVE_CLAUDE") != "1" {
		t.Skip("set MEKUGI_TEST_NATIVE_CLAUDE=1 for native journal reset acceptance")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	executable, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	version, err := exec.Command(executable, "--version").Output()
	if err != nil || strings.TrimSpace(string(version)) != "2.1.288 (Claude Code)" {
		t.Fatal("requires installed Claude 2.1.288")
	}
	bridge, err := filepath.Abs("../claude/bridge/dist/bridge.js")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	workspace, storage := t.TempDir(), t.TempDir()
	start := func(resume string) (*claude.Client, *ObservationService) {
		store, err := openMekugiReplayStore(storage)
		if err != nil {
			t.Fatal(err)
		}
		owner, err := newNativeObservationOwner(ctx, store, "claude", workspace)
		if err != nil {
			t.Fatal(err)
		}
		service, err := startObservationService(owner)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = service.Close() })
		p, err := service.PrepareCompanion(ctx)
		if err != nil {
			t.Fatal(err)
		}
		endpoint := service.Endpoint()
		client, err := claude.Start(ctx, node, bridge, claude.Config{Cwd: workspace, Executable: executable, Resume: resume, Model: "haiku", Companion: &claude.ObservationEndpoint{Socket: endpoint.Socket, Token: endpoint.Token, Plugin: p.Plugin, FrontendDirectory: p.FrontendDirectory, JournalSchema: p.JournalSchema}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = client.Close() })
		return client, service
	}
	c, s := start("")
	u := newRuntimeUI(ctx, c, "Claude Code", workspace)
	u.attachRuntimeObservation(s)
	t.Cleanup(func() { u.shell.diff.close(); u.shell.diffScreen.Close() })
	const recovery = "RECOVERY_CEDAR_76193"
	const private = "PRIVATE_NEON_28461"
	prompt := fmt.Sprintf(`This is an authorized isolated lifecycle test. Remember private conversation marker %s, but never put it in the journal or file. Use native Write once to create %s containing exactly native-reset-once and a newline. Then use mcp__mekugi__journal_batch with journal [{op:plan,reset:slice,tasks:[{title:"First slice",state:working},{title:"Report recovery marker and say prior private marker is unknown; then mark this task done",state:pending}]},{op:add,kind:context,title:"Recovery marker",body:"%s. Continue only the journal plan. Do not write files again. On the next slice, call mcp__mekugi__mchanges with args [\"--mine\",\"--summary\"] to verify inherited Write evidence, report the recovery marker and whether the private conversation marker is known, then MUST call mcp__mekugi__journal_batch with journal [{op:set,p:/2,state:done}] before your final answer. An answer claiming completion does not change task state."},{op:set,p:"/1",state:done}]. End this turn after that batch. Do not work on /2 yet. Do not call other tools except native ToolSearch if needed.`, private, filepath.Join(workspace, "native.txt"), recovery)
	phase, resets, prepared := 0, 0, 0
	writes, reads := make(map[string]bool), make(map[string]bool)
	var source, target, answer string
	for phase < 2 {
		select {
		case <-ctx.Done():
			t.Fatalf("reset acceptance timed out phase=%d reset=%d prepared=%d answer=%q", phase, resets, prepared, answer)
		case e, ok := <-c.Events():
			if !ok {
				t.Fatal("native bridge disconnected")
			}
			if e.Kind == "error" || e.Kind == "done" && e.Failed {
				t.Fatalf("native failure: %s", e.Text)
			}
			if e.Kind == "notice" && (strings.Contains(e.Text, "unavailable") || strings.Contains(e.Text, "rejected")) {
				t.Fatal(e.Text)
			}
			if e.Kind == "notice" {
				t.Log(e.Text)
			}
			if err := u.runtimeEvent(e); err != nil {
				t.Fatal(err)
			}
			switch e.Kind {
			case "ready":
				if phase == 0 && !u.runtime.busy {
					u.draft = prompt
					if _, _, err := u.runtimeKey('\r'); err != nil {
						t.Fatal(err)
					}
				}
			case "prompt":
				runtimeFrame(t, u, 120, 32)
				runtimeKeys(t, u, "1\r")
			case "session":
				if source == "" {
					source = e.SessionID
				} else if e.SessionID != source {
					target = e.SessionID
				}
			case "reset_ready":
				prepared++
				if e.Failed {
					t.Fatalf("reset failed: %s", e.Text)
				}
			case "reset":
				resets++
				target = e.SessionID
			case "tool":
				if e.Role == "Write" {
					writes[e.ID] = true
				}
				if e.Role == "mcp__mekugi__mchanges" {
					reads[e.ID] = true
				}
			case "tool_result":
				if e.Failed {
					t.Fatalf("native tool failed: %s", e.Text)
				}
			case "message":
				if phase == 1 {
					answer = e.Text
				}
			case "done":
				phase++
				if phase == 1 {
					if u.runtime.continuation == nil || u.runtime.continuation.Resume {
						t.Fatal("native completed turn did not arm slice reset")
					}
					if err := u.tickRuntimeJournal(time.Now()); err != nil {
						t.Fatal(err)
					}
					if err := u.tickRuntimeJournal(time.Now().Add(4 * time.Second)); err != nil {
						t.Fatal(err)
					}
					if u.runtime.resetRequest == "" {
						t.Fatal("slice reset was not dispatched after countdown")
					}
				}
			}
		}
	}
	if source == "" || target == "" || source == target || resets != 1 || prepared != 1 || len(writes) != 1 || len(reads) != 1 || !strings.Contains(answer, recovery) || strings.Contains(answer, private) || !strings.Contains(strings.ToLower(answer), "unknown") {
		t.Fatalf("native reset incomplete: source=%s target=%s reset=%d prepared=%d writes=%d reads=%d answer=%q", source, target, resets, prepared, len(writes), len(reads), answer)
	}
	root := s.journal.rootBinding()
	j := runtimeResetJournal(t, s, root)
	if j.Items[1].State != "done" || u.runtime.continuation != nil {
		t.Fatalf("plan state remains open: state=%s continuation=%+v", j.Items[1].State, u.runtime.continuation)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	c, s = start(target)
	ready := false
	resetAfterResume := false
	var resumedResetSession string
	answer = ""
	for {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case e, ok := <-c.Events():
			if !ok {
				t.Fatal("resumed bridge disconnected")
			}
			if e.Kind == "error" || e.Kind == "done" && e.Failed {
				t.Fatal(e.Text)
			}
			if e.Historical {
				continue
			}
			switch e.Kind {
			case "ready":
				ready = true
				if err := c.Send(ctx, "Reply with only the retained recovery marker in plain text, without Markdown. Do not use tools."); err != nil {
					t.Fatal(err)
				}
			case "tool":
				t.Fatal("resume invoked a tool")
			case "reset_ready":
				if !resetAfterResume || e.ID != "resumed-reset" || e.Failed {
					t.Fatalf("resumed reset preparation failed: %+v", e)
				}
				if err := c.Send(ctx, "Reply with only the retained recovery marker in plain text, without Markdown. Do not use tools."); err != nil {
					t.Fatal(err)
				}
			case "reset":
				if e.ID != "resumed-reset" || e.SessionID == "" || e.SessionID == target {
					t.Fatalf("resumed reset identity unavailable: %+v", e)
				}
				resumedResetSession = e.SessionID
			case "message":
				answer = e.Text
			case "done":
				// Native answer formatting is not lost context. Accept the exact
				// marker either as plain text or wrapped in Markdown backticks.
				if !ready || strings.Trim(strings.TrimSpace(answer), "`") != recovery {
					t.Fatalf("reset resume lost prompt snapshot: %q", answer)
				}
				resumed := runtimeResetJournal(t, s, s.journal.rootBinding())
				if resumed.Items[1].State != "done" || resumed.ResetIntent != nil {
					t.Fatal("resume revived plan or lost journal")
				}
				if !resetAfterResume {
					resetAfterResume, answer = true, ""
					if err := c.Reset(ctx, "resumed-reset"); err != nil {
						t.Fatal(err)
					}
					continue
				}
				if resumedResetSession == "" || s.journal.rootBinding().Session != resumedResetSession {
					t.Fatal("reset after resume did not establish its new native scope")
				}
				t.Logf("CLI %s; four native prompts; fresh query %s -> %s, then resumed reset -> %s; one Write, inherited MCP own-change read, source context absent and retained journal across both resets", strings.TrimSpace(string(version)), source, target, resumedResetSession)
				return
			}
		}
	}
}
