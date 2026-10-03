//go:build journal_e2e

package router

import (
	"context"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Enter must interrupt blocked Main before compaction, regardless of the host's
// instant-interrupt setting. The initial inference gate stays closed throughout.
func TestAppServerCompactEnterInterruptsNativeCodex(t *testing.T) {
	for _, featureFlag := range []string{"--enable", "--disable"} {
		t.Run(strings.TrimPrefix(featureFlag, "--"), func(t *testing.T) {
			provider := &appQueueProvider{requests: make(chan []byte, 8), gates: []chan struct{}{make(chan struct{})}}
			server := httptest.NewServer(responsesHandler(t.Context(), time.Minute, provider, nil, nil))
			defer server.Close()
			defer close(provider.gates[0])
			environment, workspace := routerFaultCodexEnvironment(t), t.TempDir()
			terminal := startAppResumeTerminal(t, func(ctx context.Context) *exec.Cmd {
				cmd := exec.CommandContext(ctx, "codex", "app-server", featureFlag, "instant_interrupt", "-c", `model_providers.preview={name="compact-steer",base_url=`+strconv.Quote(server.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false}`, "-c", `model_provider="preview"`, "-c", `model="gpt-6-astra"`, "-c", "features.plugins=false", "-c", "include_collaboration_mode_instructions=false")
				cmd.Env, cmd.Dir = environment, workspace
				return cmd
			}, "")
			next := func(label string) string {
				t.Helper()
				select {
				case body := <-provider.requests:
					return string(body)
				case <-terminal.ctx.Done():
					t.Fatalf("no provider request for %s: %v\n%s", label, terminal.ctx.Err(), terminal.screen.String())
				}
				return ""
			}
			terminal.await("Ready")
			terminal.send("Work that will continue after interrupting compaction\r")
			next("initial task")
			terminal.awaitMatch("active task", func(screen string) bool {
				return strings.Contains(screen, "Working") && !strings.Contains(screen, "Sending…")
			})
			terminal.send("/compact\r")
			compaction := next("compaction while initial inference remains blocked")
			if strings.Contains(compaction, compactContinuationText) {
				t.Fatal("continuation overtook compaction")
			}
			continued := next("automatic task continuation")
			if strings.Count(continued, compactContinuationText) != 1 {
				t.Fatalf("missing or repeated continuation input: %s", continued)
			}
			select {
			case <-provider.gates[0]:
				t.Fatal("initial inference was released before compaction and continuation")
			default:
			}
			terminal.await("Context compacted")
			terminal.await("Continue the task from where")
			terminal.await("╭─ Completed")
			terminal.quit()
			select {
			case body := <-provider.requests:
				t.Fatalf("continued more than once: %s", body)
			default:
			}
		})
	}
}
