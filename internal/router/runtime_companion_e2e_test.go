//go:build unix

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
	"github.com/yusing/mekugi/internal/session"
)

// Opt-in real prompts and native compaction, with normal native billing/settings.
// The utility executes in Claude's Bash, not the utility service or UI.
func TestRuntimeCompanionClaudeLive(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_NATIVE_CLAUDE") != "1" {
		t.Skip("requires authenticated native Claude acceptance")
	}
	nativeAcceptanceSettingsUnchanged(t)
	t.Setenv(routerTestWorkerEnvironment, "1")
	t.Setenv("MEKUGI_RUNTIME_DIR", t.TempDir())
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	executable, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := filepath.Abs("../claude/bridge/dist/bridge.js")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	service, binding, _ := observationHTTPFixture(t)
	presentation, err := service.PrepareCompanion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Native agents retain their own tool restrictions. This test explicitly
	// grants the fixture agent MCP tools through a local plugin, never by
	// rewriting an existing user agent or inventing SDK caller metadata.
	agents := filepath.Join(presentation.Plugin, "agents")
	if err := os.Mkdir(agents, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agents, "journal-acceptance.md"), []byte("---\nname: journal-acceptance\ndescription: Isolated native companion journal acceptance.\ntools: ToolSearch, mcp__mekugi__journal_batch\nmodel: haiku\n---\nCall mcp__mekugi__journal_batch exactly once with journal [{op: add, kind: task, title: Child journal acceptance, state: working}]. Native ToolSearch may discover the tool. Then return CHILD_ACCEPTED. If unavailable, return MCP_UNAVAILABLE. No other tools.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	endpoint := service.Endpoint()
	client, err := claude.Start(ctx, node, bridge, claude.Config{Cwd: binding.Workspace, Executable: executable, Model: "haiku", Companion: &claude.ObservationEndpoint{Socket: endpoint.Socket, Token: endpoint.Token, Plugin: presentation.Plugin, FrontendDirectory: presentation.FrontendDirectory, JournalSchema: presentation.JournalSchema}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ready := false
	phase := 0
	recoveries := 0
	writes := make(map[string]bool)
	implicitReads := make(map[string]bool)
	var nativeSession string
	var recoveryAnswer string
	var childOutcome string
	first := fmt.Sprintf("This is an authorized isolated companion acceptance. Use native Write exactly once to create %s with content exactly native-once followed by a newline. Then call mcp__mekugi__journal_batch once with journal [{op:add,kind:context,title:Native marker,body:Preserve MEKUGI_RECOVERY_CEDAR},{op:add,kind:task,title:Verify recovery,state:working}]. Call journal_read once. Call mcp__mekugi__mchanges with args [\"--mine\",\"--summary\"] exactly once to review your own captured Write. Run mcat native.txt through native Bash, then run mchanges with the explicit Write change ID from the companion hook and --summary. Finally run mchanges revert with that same explicit Write ID exactly once through Bash. Do not use Read, Edit, other commands or subagents. Do not repair or retry failing tools. Reply only ACCEPTED.", filepath.Join(binding.Workspace, "native.txt"))
	childrenSettled := func() bool {
		service.owner.mu.Lock()
		var bindings []ObservationBinding
		for b := range service.owner.bindings {
			if b.Agent != "" {
				bindings = append(bindings, b)
			}
		}
		service.owner.mu.Unlock()
		if len(bindings) != 2 {
			return false
		}
		for _, b := range bindings {
			childCtx, err := service.journal.scope(ctx, b)
			if err != nil {
				return false
			}
			child, found, err := readThreadJournal(service.owner.store.scoped(childCtx), b.Workspace, observationThread(b))
			if err != nil || !found || !child.IdentityKnown || child.LifecycleState != "done" {
				return false
			}
		}
		return true
	}
	for phase < 4 || !childrenSettled() {
		select {
		case <-ctx.Done():
			t.Fatalf("native companion acceptance timed out; native child outcome=%q", childOutcome)
		case <-time.After(100 * time.Millisecond):
			// Root completion does not settle independently running children.
		case e, ok := <-client.Events():
			if !ok {
				t.Fatal("native bridge disconnected")
			}
			if e.Kind == "session" && e.SessionID != "" {
				nativeSession = e.SessionID
			}
			switch e.Kind {
			case "ready":
				if !ready {
					ready = true
					if err := client.Send(ctx, first); err != nil {
						t.Fatal(err)
					}
				}
			case "prompt":
				if e.Prompt == nil {
					t.Fatal("missing native permission")
				}
				if err := client.Respond(ctx, session.Decision{ID: e.Prompt.ID, Allow: true}); err != nil {
					t.Fatal(err)
				}
			case "tool":
				if e.Role == "mcp__mekugi__mchanges" {
					implicitReads[e.ID] = true
				}
				if e.Role == "Write" {
					writes[e.ID] = true
				}
			case "tool_result":
				if e.Failed {
					t.Fatalf("native tool failed: %s", e.Text)
				}
			case "message":
				if phase == 2 {
					recoveryAnswer = e.Text // Native message events replace streamed text.
				}
				if phase >= 3 {
					childOutcome = e.Text
				}
			case "notice":
				if strings.Contains(e.Text, "unavailable") || strings.Contains(e.Text, "rejected") {
					t.Fatalf("companion unavailable: %s", e.Text)
				}
				if strings.HasPrefix(e.Text, "Journal recovery prepared") {
					recoveries++
					t.Log(e.Text)
				}
			case "error":
				t.Fatal(e.Text)
			case "done":
				if e.Failed {
					t.Fatal(e.Text)
				}
				phase++
				if phase == 1 {
					if err := client.Send(ctx, "/compact"); err != nil {
						t.Fatal(err)
					}
				}
				if phase == 2 {
					if err := client.Send(ctx, "Answer only the MEKUGI_RECOVERY marker from retained journal context. No tools."); err != nil {
						t.Fatal(err)
					}
				}
				if phase == 3 {
					if err := client.Send(ctx, "Launch exactly two native Agent children concurrently using subagent_type mekugi:journal-acceptance and run_in_background=false. Give each this exact task: Call mcp__mekugi__journal_batch once with journal [{op: add, kind: task, title: Child journal acceptance, state: working}], then return CHILD_ACCEPTED. Native ToolSearch may discover the MCP tool; do not call journal_read, modify files or use other tools. Do not use general-purpose, Explore or Plan agents. Wait for both children's actual completion and report their outcomes. This is an authorized journal isolation acceptance."); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
	}
	// The prompt already names the marker's prefix; CEDAR is the retained fact
	// it does not supply. Native answers may return that value or the full ID.
	answer := strings.TrimSpace(recoveryAnswer)
	if len(implicitReads) != 1 {
		t.Fatalf("native implicit read count = %d", len(implicitReads))
	}
	if len(writes) != 1 || recoveries != 1 || answer != "CEDAR" && answer != "MEKUGI_RECOVERY_CEDAR" {
		t.Fatalf("acceptance incomplete: writes=%d recovery=%d answer=%q", len(writes), recoveries, answer)
	}
	if _, err := os.Stat(filepath.Join(binding.Workspace, "native.txt")); !os.IsNotExist(err) {
		t.Fatal("native revert did not remove the original file")
	}
	root := service.journal.rootBinding()
	if root.Session != nativeSession {
		t.Fatal("native identity not retained")
	}
	scoped, err := service.journal.scope(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	journal, exists, err := readThreadJournal(service.owner.store.scoped(scoped), root.Workspace, observationThread(root))
	if err != nil || !exists || len(journal.Items) != 2 || journal.Items[0].Body != "Preserve MEKUGI_RECOVERY_CEDAR" || journal.Items[1].State != "working" {
		t.Fatalf("journal completion fabricated task state: %+v %v", journal, err)
	}
	service.owner.mu.Lock()
	var children []ObservationBinding
	for b := range service.owner.bindings {
		if b.Agent != "" {
			children = append(children, b)
		}
	}
	service.owner.mu.Unlock()
	if len(children) != 2 {
		t.Fatalf("expected two independently bound native child journals, got %d", len(children))
	}
	for _, b := range children {
		childCtx, err := service.journal.scope(ctx, b)
		if err != nil {
			t.Fatal(err)
		}
		child, exists, err := readThreadJournal(service.owner.store.scoped(childCtx), b.Workspace, observationThread(b))
		if err != nil || !exists || !child.IdentityKnown || child.Parent != observationThread(root) || child.LifecycleState != "done" || len(child.Items) != 1 || child.Items[0].State != "working" {
			t.Fatalf("native child parent/lifecycle evidence incomplete: %+v %v; native outcomes=%q", child, err, childOutcome)
		}
	}
	index, err := service.owner.store.scoped(scoped).readChangeIndex(root.Workspace)
	if err != nil || len(index.Changes) != 2 {
		t.Fatalf("expected one Write and one revert capture: %d %v", len(index.Changes), err)
	}
	t.Logf("Native companion accepted: session=%s changes=%d, original Write and native Bash revert once", root.Session, len(index.Changes))
}
