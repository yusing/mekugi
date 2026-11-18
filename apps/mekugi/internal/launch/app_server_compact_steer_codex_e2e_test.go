//go:build codex_e2e

package launch

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
	native, err := exec.LookPath("mekugi-app-server")
	if err != nil {
		t.Fatal("managed native app-server is required:", err)
	}
	for _, instant := range []bool{true, false} {
		t.Run(strconv.FormatBool(instant), func(t *testing.T) {
			provider := &appQueueProvider{requests: make(chan []byte, 8), gates: []chan struct{}{make(chan struct{})}}
			server := httptest.NewServer(testResponsesHandler(t.Context(), time.Minute, provider, nil, nil))
			defer server.Close()
			defer close(provider.gates[0])
			environment, workspace := routerFaultCodexEnvironment(t), t.TempDir()
			terminal := startAppResumeTerminal(t, func(ctx context.Context) *exec.Cmd {
				cmd := exec.CommandContext(ctx, native, "-c", "features.instant_interrupt="+strconv.FormatBool(instant), "-c", `model_providers.preview={name="compact-steer",base_url=`+strconv.Quote(server.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false}`, "-c", `model_provider="preview"`, "-c", `model="gpt-6-astra"`, "-c", "features.plugins=false")
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
			continued := next("automatic task continuation")
			if strings.Contains(continued, "Continue the task from where") {
				t.Fatalf("fabricated continuation input: %s", continued)
			}
			select {
			case <-provider.gates[0]:
				t.Fatal("initial inference was released before compaction and continuation")
			default:
			}
			terminal.await("Context reset")
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
