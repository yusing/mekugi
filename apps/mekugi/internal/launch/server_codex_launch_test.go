package launch

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestCodexLaunchIgnoresThirdPartyCredentials(t *testing.T) {
	for _, key := range []string{"HOME", "CODEX_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "MEKUGI_RUNTIME_DIR"} {
		t.Setenv(key, t.TempDir())
	}
	t.Setenv("XAI_API_KEY", "grok-test")
	t.Setenv("OPENCODE_API_KEY", "shared-test")
	t.Setenv("OPENCODE_GO_API_KEY", "invalid\nunused")
	t.Setenv("OPENCODE_ZEN_API_KEY", "zen-test")
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	ready := make(chan invocationFixture, 1)
	done := make(chan error, 1)
	go func() {
		done <- testRunSession(testHistory(), testRecovery(), ctx, testCommands(), nil, nil, func(session invocationFixture) { ready <- session }, nil)
	}()
	var session invocationFixture
	select {
	case session = <-ready:
	case err := <-done:
		t.Fatalf("Codex router stopped before ready: %v", err)
	case <-ctx.Done():
		t.Fatal("Codex router did not start")
	}
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Codex router shutdown: %v", err)
		}
	}()

	client := &http.Client{Timeout: 5 * time.Second}
	for _, model := range []string{"grok-4.7", "grok:grok-4.7", "opencode-go:glm-5.3", "opencode-zen:kimi-k3"} {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, session.BaseURL+"/responses",
			strings.NewReader(`{"model":"`+model+`","input":[]}`))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode < 400 || !strings.Contains(string(body), "require") || !strings.Contains(string(body), "mekugi") {
			t.Fatalf("Codex accepted third-party model %s: status=%d body=%s error=%v", model, response.StatusCode, body, err)
		}
	}
}
