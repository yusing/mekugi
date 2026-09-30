//go:build journal_e2e

package router

import (
	"context"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/yusing/mekugi/internal/appserver"
)

type appSettingsProvider struct {
	requests chan []byte
	release  chan struct{}
	once     sync.Once
}

func (p *appSettingsProvider) forwardExecution(ctx, _ context.Context, body []byte, _ http.Header, _ string) (*http.Response, error) {
	p.requests <- append([]byte(nil), body...)
	var err error
	p.once.Do(func() {
		select {
		case <-p.release:
		case <-ctx.Done():
			err = ctx.Err()
		}
	})
	if err != nil {
		return nil, err
	}
	return routerFaultCodexSuccessResponse(), nil
}

// A captured inference stays blocked while real Codex accepts the live update.
// The following turn proves that defaults reached actual provider requests.
func TestAppServerLiveSettingsNativeCodex(t *testing.T) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	p := &appSettingsProvider{requests: make(chan []byte, 8), release: make(chan struct{})}
	server := httptest.NewServer(responsesHandler(t.Context(), time.Minute, p, nil, nil))
	defer server.Close()
	defer func() {
		select {
		case <-p.release:
		default:
			close(p.release)
		}
	}()
	environment, workspace := routerFaultCodexEnvironment(t), t.TempDir()
	terminal := startAppResumeTerminal(t, func(ctx context.Context) *exec.Cmd {
		cmd := exec.CommandContext(ctx, codex, "app-server", "-c", `model_providers.preview={name="OpenAI",base_url=`+strconv.Quote(server.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false}`, "-c", `model_provider="preview"`, "-c", `model="gpt-6-astra"`, "-c", `model_reasoning_effort="low"`, "-c", "features.plugins=false", "-c", "include_collaboration_mode_instructions=false")
		cmd.Env, cmd.Dir = environment, workspace
		return cmd
	}, "")
	if err := pty.Setsize(terminal.outer, &pty.Winsize{Cols: 200, Rows: 30}); err != nil {
		t.Fatal(err)
	}
	terminal.await("Ready")
	terminal.send("First captured inference\r")
	select {
	case <-p.requests:
	case <-terminal.ctx.Done():
		t.Fatal(terminal.ctx.Err())
	}
	terminal.await("Working")
	terminal.send("/effort\r")
	terminal.await("Choose effort")
	terminal.await("enter apply")
	terminal.send("\x1b[B\x1b[B\r")
	terminal.await("live update published")
	terminal.send("/model gpt-6-sol\r")
	terminal.await("gpt-6-sol (high)")
	// Wait for the turn publication rather than just the thread's new footer.
	terminal.await("live update published")
	terminal.send("/model gpt-6-astra\r")
	terminal.await("gpt-6-astra (high)")
	terminal.await("live update published")
	terminal.send("/tier priority\r")
	terminal.await("gpt-6-astra (high) · priority")
	terminal.await("live update published")
	close(p.release)
	terminal.await("completed")
	terminal.send("Second inference uses saved settings\r")
	var body []byte
	select {
	case body = <-p.requests:
	case <-terminal.ctx.Done():
		t.Fatal(terminal.ctx.Err())
	}
	var request struct {
		Input []struct {
			Type      string `json:"type"`
			Reasoning struct {
				Effort string `json:"effort"`
			} `json:"reasoning"`
		} `json:"input"`
		Model     string `json:"model"`
		Tier      string `json:"service_tier"`
		Reasoning struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatal(err)
	}
	if request.Model != "gpt-6-astra" || request.Tier != "priority" || request.Reasoning.Effort != "low" {
		t.Fatalf("provider settings: %+v", request)
	}
	foundUpdate := false
	for _, item := range request.Input {
		if item.Type == "configuration_update" && item.Reasoning.Effort == "high" {
			foundUpdate = true
		}
	}
	if !foundUpdate {
		t.Fatal("Codex did not author the high-effort configuration_update")
	}
	terminal.await("Second inference uses saved settings")
	terminal.await("completed")
	terminal.quit()
}

func TestAppServerSettingsCatalogNativeCodex(t *testing.T) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(responsesHandler(t.Context(), time.Minute, &appPreviewProvider{}, nil, nil))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, codex, "app-server", "-c", `model_providers.preview={name="preview",base_url=`+strconv.Quote(server.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false}`, "-c", `model_provider="preview"`, "-c", `model="gpt-6-astra"`, "-c", "features.plugins=false", "-c", "include_collaboration_mode_instructions=false")
	cmd.Env, cmd.Dir = routerFaultCodexEnvironment(t), t.TempDir()
	client, err := appserver.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	u := &appServerUI{client: client, requests: make(map[string]string), view: newLiveActivityView(), agents: newLiveActivityView()}
	if err := u.request("initialize", nil); err != nil {
		t.Fatal(err)
	}
	for u.thread == "" || u.modelsLoading {
		select {
		case message := <-client.Messages:
			if err := u.message(message); err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if len(u.effortChoices()) == 0 {
		t.Fatalf("model=%q catalog=%+v notice=%q", u.model, u.models, u.notice)
	}
}
