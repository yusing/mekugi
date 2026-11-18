//go:build codex_e2e

package launch

import (
	"context"
	"crypto/sha256"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAppServerCancelManualResetNativeCodex(t *testing.T) {
	// Hold source acquisition behind the actual pre-compact hook, so that
	// cancel is not racing a nearly instantaneous summary-free reset.
	started, release := make(chan struct{}), make(chan struct{})
	gate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hold" {
			http.NotFound(w, r)
			return
		}
		close(started)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer gate.Close()
	defer close(release)
	command := "curl -fsS " + quoteShellWord(gate.URL+"/hold")
	handler := map[string]any{"type": "command", "command": command, "timeout": 30, "async": false}
	identity, err := json.Marshal(map[string]any{"event_name": "pre_compact", "matcher": "manual", "hooks": []any{handler}}, json.Deterministic(true))
	if err != nil {
		t.Fatal(err)
	}
	trust := fmt.Sprintf(`hooks.state={"/<session-flags>/config.toml:pre_compact:0:0"={trusted_hash="sha256:%x",enabled=true}}`, sha256.Sum256(identity))
	environment, workspace := routerFaultCodexEnvironment(t), t.TempDir()
	terminal := startAppResumeTerminal(t, func(ctx context.Context) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "mekugi-app-server", "-c", `model_providers.preview={name="compact-cancel",base_url=`+strconv.Quote(gate.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false}`, "-c", `model_provider="preview"`, "-c", `model="gpt-6-astra"`, "-c", "features.plugins=false", "-c", "features.hooks=true", "-c", `hooks.PreCompact=[{matcher="manual",hooks=[{type="command",command=`+strconv.Quote(command)+`,timeout=30,async=false}]}]`, "-c", trust)
		cmd.Env, cmd.Dir = environment, workspace
		return cmd
	}, "")
	terminal.await("Ready")
	terminal.send("/compact\r")
	select {
	case <-started:
	case <-terminal.ctx.Done():
		t.Fatal("manual reset did not reach its pre-compact hook:", terminal.screen.String())
	}
	terminal.send("\x03")
	terminal.await("Compaction canceled")
	terminal.quit()
}

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
