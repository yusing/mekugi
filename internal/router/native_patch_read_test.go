package router

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/internal/execscope"
	"github.com/yusing/mekugi/libraries/commandanalysis"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/modules/changehistory/changes"
)

func TestCodeModeLiteralPatchRetainsNeutralReadersThroughReplay(t *testing.T) {
	for _, tc := range []struct {
		name, command string
		exit, repeats int
	}{
		{"net_and_rg", "mchanges ash1..ash3 --net; rg -n 'image' file.txt", 1, 1},
		{"mcat_and_summary_loop", "mcat file.txt 1:20; mchanges ash1..ash3 --summary", 0, 2},
		{"skills_get", "skills-mgr get golang-best-practices", 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			attachTestReplayStore(t, proxy)
			trace := newNativeTraceFixture(t)
			proxy.nativeTrace = &nativeToolTrace{directory: trace.root}
			transform, _, _, workspace := newMekugiTestTransformWithProxy(t, proxy)
			transform.sessionShell = "bash"
			target := filepath.Join(workspace, "file.txt")
			writeTestFile(t, target, "before\n")

			patch := "*** Begin Patch\n*** Update File: file.txt\n@@\n-before\n+after\n*** End Patch\n"
			source := "await tools.apply_patch(" + string(responses.MustMarshalJSON(patch)) + "); " +
				fmt.Sprintf("for (let i = 0; i < %d; i++) await tools.exec_command({cmd: %s});", tc.repeats, responses.MustMarshalJSON(tc.command))
			call := map[string]any{
				"type": "custom_tool_call", "id": "reader-cell-item", "call_id": "reader-cell",
				"name": "exec", "input": source, "status": "completed",
			}
			if _, err := transform.TransformJSON(mustTestJSON(t, map[string]any{
				"id": "response", "status": "completed", "output": []any{call},
			})); err != nil {
				t.Fatal(err)
			}
			lookup := func(p *mekugiProxy, id string) mekugiHistory {
				t.Helper()
				history, found, err := p.replayStore.lookup(transform.ctx, workspace, id)
				if err != nil || !found {
					t.Fatalf("lookup %s: found=%v err=%v", id, found, err)
				}
				return history
			}
			// Inspect durable pre-call metadata, not just the classifier in isolation.
			before := lookup(proxy, "reader-cell")
			if before.ResolvedBaseline != nil {
				t.Fatal("literal patch and neutral reader unnecessarily inventoried the workspace")
			}
			observation := before.ExecObservation
			wantCommand := commandanalysis.Command{Command: tc.command, Workdir: workspace, Shell: "bash"}
			if observation == nil || observation.Class != execscope.Neutral.String() ||
				!slices.Equal(observation.Commands, []commandanalysis.Command{wantCommand}) ||
				!slices.Equal(observation.commandClasses(), []string{execscope.Neutral.String()}) {
				t.Fatalf("pre-call neutral command/default shell not retained: %+v", observation)
			}

			trace.start("thread-1", "runtime-1", "reader-cell", source)
			trace.tool("thread-1", "runtime-1", "real-patch", "apply_patch", patch)
			trace.result("thread-1", "real-patch", "completed", map[string]any{})
			for i := range tc.repeats {
				id := fmt.Sprintf("reader-%d", i)
				// Host arguments omit shell, so matching must recover its pre-call identity.
				trace.tool("thread-1", "runtime-1", id, "exec_command", string(responses.MustMarshalJSON(map[string]any{"cmd": tc.command})))
				trace.result("thread-1", id, "completed", map[string]any{"exit_code": tc.exit})
			}
			trace.end("thread-1", "runtime-1")
			writeTestFile(t, target, "after\n")
			items := []any{call, map[string]any{
				"type": "custom_tool_call_output", "call_id": "reader-cell", "output": []any{
					map[string]any{"type": "input_text", "text": "Script completed\nWall time 0.1 seconds\nOutput:\n"},
				},
			}}
			reconcile := func(p *mekugiProxy) {
				t.Helper()
				request, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{"input": items}))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := p.reconcileVisibleInput(transform.ctx, &request, workspace, transform.historySessionID); err != nil {
					t.Fatal(err)
				}
			}
			reconcile(proxy)
			patchHistory := lookup(proxy, nativePatchDerivedCallID("reader-cell", 0))
			if patchHistory.ChangeID == "" || patchHistory.TranslationError != "" || len(patchHistory.ReviewFiles) != 1 {
				t.Fatalf("real patch evidence missing: %+v", patchHistory)
			}
			diff, err := proxy.replayStore.changes.Read(transform.ctx, proxy.replayStore.scoped(transform.ctx).changeScope(), changes.Query{
				Workspace: workspace, IDs: []string{patchHistory.ChangeID}, MaxTokens: 4000,
			})
			if err != nil || !strings.Contains(diff, "-before") || !strings.Contains(diff, "+after") {
				t.Fatalf("patch evidence = %q, %v", diff, err)
			}
			assertCommandAndIDs := func(p *mekugiProxy) {
				t.Helper()
				command := lookup(p, execDerivedCallID("reader-cell", true))
				status := execStatusCompleted
				if tc.exit != 0 {
					status = execStatusFailed
				}
				if command.ExecOutcome == nil || command.ExecOutcome.Status != status ||
					command.ExecOutcome.Coverage != execCoverageExact || command.ExecOutcome.Class != execscope.Neutral.String() ||
					command.ChangeID != "" || len(command.ReviewFiles) != 0 || command.TranslationError != "" {
					t.Fatalf("reader acquired incomplete or patch effects: %+v", command)
				}
				if len(command.HostResults) != tc.repeats || strings.Count(command.Script, tc.command) != tc.repeats ||
					strings.Contains(command.ExecOutcome.ScopeReason, "intermediate writes") {
					t.Fatalf("reader repetition lost results or invented intermediate writes: %+v", command)
				}
				index, err := p.replayStore.scoped(transform.ctx).changes.Inspect(p.replayStore.scoped(transform.ctx).changeScope(), workspace)
				if err != nil {
					t.Fatal(err)
				}
				if _, found := index.Changes[patchHistory.ChangeID]; !found || len(index.Changes) != 1 {
					t.Fatalf("wanted only patch change ID %s, got %v", patchHistory.ChangeID, index.Changes)
				}
			}
			assertCommandAndIDs(proxy)

			// Resume in a fresh proxy/store without live native trace or in-memory history.
			resumed := newManagedMekugiProxy(t)
			resumed.replayStore, err = openMekugiReplayStore(proxy.replayStore.directory)
			if err != nil {
				t.Fatal(err)
			}
			reconcile(resumed)
			assertCommandAndIDs(resumed)
			if replayed := lookup(resumed, nativePatchDerivedCallID("reader-cell", 0)); replayed.ChangeID != patchHistory.ChangeID {
				t.Fatalf("resume replaced patch change ID: %+v", replayed)
			}
		})
	}
}
