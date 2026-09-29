//go:build journal_e2e

package router

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// appQueueProvider holds each gated inference until the test releases it.
type appQueueProvider struct {
	requests chan []byte
	gates    []chan struct{}
	mu       sync.Mutex
	count    int
}

func (p *appQueueProvider) forwardExecution(ctx, _ context.Context, body []byte, _ http.Header, _ string) (*http.Response, error) {
	p.mu.Lock()
	index := p.count
	p.count++
	p.mu.Unlock()
	p.requests <- bytes.Clone(body)
	if index < len(p.gates) {
		select {
		case <-p.gates[index]:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return routerFaultCodexSuccessResponse(), nil
}

// Real Codex discards uncommitted steers on interrupt, commits accepted steers
// at the next sampling boundary, and starts queued input as its own turn.
func TestAppServerSteerAndQueueNativeCodex(t *testing.T) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	p := &appQueueProvider{requests: make(chan []byte, 8), gates: []chan struct{}{make(chan struct{}), make(chan struct{})}}
	server := httptest.NewServer(responsesHandler(t.Context(), time.Minute, p, nil, nil, nil))
	defer server.Close()
	defer func() {
		for _, gate := range p.gates {
			select {
			case <-gate:
			default:
				close(gate)
			}
		}
	}()
	environment, workspace := routerFaultCodexEnvironment(t), t.TempDir()
	terminal := startAppResumeTerminal(t, func(ctx context.Context) *exec.Cmd {
		cmd := exec.CommandContext(ctx, codex, "app-server", "-c", `model_providers.preview={name="Native control fixture",base_url=`+strconv.Quote(server.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false}`, "-c", `model_provider="preview"`, "-c", `model="mock-model"`, "-c", "features.plugins=false", "-c", "include_collaboration_mode_instructions=false")
		cmd.Env, cmd.Dir = environment, workspace
		return cmd
	}, "")
	next := func(label string) string {
		t.Helper()
		select {
		case body := <-p.requests:
			return string(body)
		case <-terminal.ctx.Done():
			t.Fatalf("no provider request for %s: %v\n%s", label, terminal.ctx.Err(), terminal.screen.String())
		}
		return ""
	}
	settled := func(label string) {
		t.Helper()
		terminal.awaitMatch(label, func(screen string) bool {
			return strings.Contains(screen, "Working") && !strings.Contains(screen, "Sending…")
		})
	}
	terminal.await("Ready")
	terminal.send("First blocked turn\r")
	next("first turn")
	settled("first turn running")

	terminal.send("steer interrupts\r")
	settled("steer accepted")
	terminal.await("↳ steer interrupts")
	terminal.send("queued afterwards\t")
	terminal.await("Queued for the next turn")
	terminal.send("\x03")
	terminal.awaitMatch("interrupted input restored", func(screen string) bool {
		return strings.Contains(screen, "╭─ Interrupted") && strings.Contains(screen, "steer interrupts") && strings.Contains(screen, "queued afterwards") && !strings.Contains(screen, "↳")
	})
	select {
	case body := <-p.requests:
		t.Fatalf("interruption automatically resent input: %s", body)
	default:
	}
	terminal.send("\r")
	resent := next("explicit resubmission")
	if strings.Count(resent, "steer interrupts") != 1 || !strings.Contains(resent, "queued afterwards") {
		t.Fatalf("restored input request: %s", resent)
	}
	settled("resubmitted turn running")
	terminal.send("queued later\t")

	terminal.send("steer commits\r")
	settled("second steer accepted")
	terminal.await("↳ steer commits")
	close(p.gates[1])
	if committed := next("committed steer"); strings.Count(committed, "steer commits") != 1 {
		t.Fatalf("steer not committed once: %s", committed)
	}
	if queued := next("queued turn"); strings.Count(queued, "queued later") != 1 || strings.Count(queued, "steer interrupts") != 1 {
		t.Fatalf("queued turn request: %s", queued)
	}
	terminal.awaitMatch("pending input cleared", func(screen string) bool {
		return strings.Contains(screen, "╭─ Completed") && !strings.Contains(screen, "↳") && strings.Count(screen, "steer commits") == 1
	})
	terminal.send("/compact\r")
	next("manual compaction")
	terminal.await("Context compacted")
	terminal.await("╭─ Completed")
	terminal.send("/clear\r")
	terminal.awaitMatch("fresh session", func(screen string) bool {
		return strings.Contains(screen, "Ready") && !strings.Contains(screen, "steer interrupts") && !strings.Contains(screen, "queued later")
	})
	terminal.send("Fresh session prompt\r")
	fresh := next("fresh session")
	if !strings.Contains(fresh, "Fresh session prompt") || strings.Contains(fresh, "steer interrupts") {
		t.Fatalf("clear retained prior context: %s", fresh)
	}
	terminal.await("╭─ Completed")
	terminal.quit()
}
