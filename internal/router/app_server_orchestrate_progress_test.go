package router

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/orchestrate"
)

func TestAppServerOrchestrateIndependentBatchProgress(t *testing.T) {
	for _, layer := range []string{"store", "router"} {
		t.Run(layer, func(t *testing.T) {
			workspace := t.TempDir()
			writeTestFile(t, filepath.Join(workspace, "file"), "base")
			u := orchestrateCleanupUIInWorkspace(t, workspace)
			s, a := u.proxy.orchestration.store, u.orchestrateThreads["child"]
			gitTestRun(t, a.batch.Cwd, "config", "user.name", "test")
			gitTestRun(t, a.batch.Cwd, "config", "user.email", "test@example.invalid")
			writeTestFile(t, filepath.Join(a.batch.Cwd, "file"), "integrated")
			gitTestCommit(t, a.batch.Cwd)
			other, err := s.Prepare(t.Context(), workspace, "main", "other")
			if err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			snapshot := s.ShadowSnapshot
			s.ShadowSnapshot = func(ctx context.Context, source, repository string) (string, error) {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					return "", ctx.Err()
				}
				return snapshot(ctx, source, repository)
			}
			effect := &orchestrateCommand{ctx: t.Context(), workspace: workspace, main: "main", tool: "integrate", target: "batch", reply: make(chan orchestrateResult, 1)}
			if layer == "router" {
				u.startOrchestratedChild(effect)
			} else {
				go func() {
					b, err := s.IntegrateShadow(t.Context(), workspace, "main", "batch", "child")
					effect.reply <- orchestrateResult{batch: b, err: err}
				}()
			}
			unblock := sync.OnceFunc(func() { close(release) })
			defer func() {
				unblock()
				drainOrchestrateWork(t, u)
				var result orchestrateResult
				select {
				case result = <-effect.reply:
				case <-time.After(5 * time.Second):
					t.Fatal("integration did not finish after release")
				}
				if result.err != nil || result.batch.Integration == nil {
					t.Error("integration failed after independent progress", result.err)
				}
				retained, err := (&orchestrate.Store{Directory: s.Directory}).Snapshot(workspace, "main")
				if err != nil {
					t.Fatal(err)
				}
				for _, b := range retained {
					if !t.Failed() && b.TaskName == "other" && (b.Launch.HostStatus != "completed" || b.Launch.TurnID != "other-turn" || b.Workflow.Outcome == nil || b.Workflow.Outcome.State != orchestrate.DispatchAcknowledged || b.Workflow.Outcome.Message != "Exact independent answer") {
						t.Fatal("integration publication lost independent durable completion", b)
					}
				}

			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("integration did not enter its long snapshot effect")
			}
			// A different store instance must still order the held batch's writes.
			ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
			err = (&orchestrate.Store{Directory: s.Directory}).ObserveLaunch(ctx, workspace, "main", "batch", "failed", "out of order")
			cancel()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("same-batch persistence passed its unfinished effect", err)
			}
			wire := u.client.Input.(*appServerTestInput)
			pump := func(done func() bool) {
				t.Helper()
				timer := time.NewTimer(2 * time.Second)
				defer timer.Stop()
				for !done() {
					select {
					case complete := <-u.orchestrateCompletions:
						u.completeOrchestrateWork(complete)
					case <-timer.C:
						t.Fatal("independent batch progress waited for integration")
					}
				}
				if len(effect.reply) != 0 {
					t.Fatal("integration was not held during independent progress")
				}
			}
			spawn := &orchestrateCommand{ctx: t.Context(), workspace: workspace, main: "main", tool: "spawn_agent", input: orchestrateSpawnInput{TaskName: "other", Kind: orchestrate.WorkflowDiagnosis, Message: "diagnose"}, reply: make(chan orchestrateResult, 1)}
			u.startOrchestratedChild(spawn)
			pump(func() bool { return wire.Len() != 0 })
			start := btwTestRequest(t, wire, "thread/start", "")
			btwTestReply(t, u, start, fmt.Sprintf(`{"thread":{"id":"other-thread","cwd":%q},"sandbox":{"type":"readOnly"},"approvalPolicy":"never"}`, other.Cwd))
			pump(func() bool { return wire.Len() != 0 })
			turn := btwTestRequest(t, wire, "turn/start", "other-thread")
			// Completion and answer arrive before the first-turn acknowledgement.
			appServerTestMessage(t, u, `{"method":"item/completed","params":{"threadId":"other-thread","turnId":"other-turn","item":{"type":"agentMessage","text":"Exact independent answer"}}}`)
			appServerTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"other-thread","turn":{"id":"other-turn","status":"completed"}}}`)
			btwTestReply(t, u, turn, `{"turn":{"id":"other-turn"}}`)
			pump(func() bool { return wire.Len() != 0 && len(spawn.reply) != 0 })
			if result := <-spawn.reply; result.err != nil {
				t.Fatal(result.err)
			}
			answer, params := workflowTake(t, u, wire, "thread/inject_items", "main", "")
			text := params["items"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"]
			if text != "Message Type: FINAL_ANSWER\nTask name: /root\nSender: /root/other\nPayload:\nExact independent answer" {
				t.Fatal("answer changed identity or bytes", text)
			}
			btwTestReply(t, u, answer, `{}`)
			pump(func() bool {
				return u.orchestrateThreads["other-thread"].batch.Workflow.Outcome.State == orchestrate.DispatchAcknowledged
			})
			wait := &orchestrateCommand{ctx: t.Context(), workspace: workspace, main: "main", tool: "wait_agent", callID: "independent", waitTargets: []string{"other"}, reply: make(chan orchestrateResult, 1)}
			u.startOrchestratedChild(wait)
			pump(func() bool { return len(wait.reply) != 0 })
			if result := <-wait.reply; result.err != nil || waitTestEvent(t, result).Task != "other" {
				t.Fatal("independent completion was not delivered", result.err)
			}
		})
	}
}
