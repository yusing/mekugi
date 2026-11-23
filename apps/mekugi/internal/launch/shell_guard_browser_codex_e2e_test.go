//go:build codex_e2e

package launch

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/apps/mekugi/internal/connector"
	"github.com/yusing/mekugi/apps/mekugi/internal/webui"
)

// A single native command is approved through the browser, authenticated in
// the terminal, then observed by the model. No privileged command is executed.
func TestShellGuardBrowserAndTerminalNativeCodex(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_WEBUI_BROWSER") != "1" {
		t.Skip("set MEKUGI_TEST_WEBUI_BROWSER=1 after make webui")
	}
	t.Setenv("AGENT_BROWSER_ENGINE", "chrome")
	browser := browserFixtureCommand(t)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	identity, err := exec.CommandContext(ctx, browser, "session", "id", "--scope", "worktree", "--prefix", "shell-guard").CombinedOutput()
	if err != nil {
		t.Fatalf("browser identity: %v: %s", err, identity)
	}
	session := strings.TrimSpace(string(identity))
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = exec.CommandContext(cleanup, browser, "--session", session, "close").Run()
	}()
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.CommandContext(ctx, browser, append([]string{"--session", session}, args...)...).CombinedOutput(); err != nil {
			snapshot, _ := exec.CommandContext(ctx, browser, "--session", session, "snapshot").CombinedOutput()
			t.Fatalf("browser %s: %v %s\n%s", args[0], err, out, snapshot)
		}
	}
	page := httptest.NewServer(webui.Handler())
	defer page.Close()
	run("open", page.URL)
	run("set", "viewport", "1280", "900")
	const token = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	tokenFile := filepath.Join(shortTempDir(t), "token")
	if err := os.WriteFile(tokenFile, []byte(token+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run("find", "label", "Machine name", "fill", "Guard")
	run("find", "label", "Access token", "fill", token)

	const secret = "fixture-only-value"
	fakeSudo := filepath.Join(t.TempDir(), "sudo")
	if err := os.WriteFile(fakeSudo, []byte("#!/bin/bash\n[[ $1 == -A && $(\"$SUDO_ASKPASS\") == "+secret+" ]] && printf AUTHENTICATED"), 0700); err != nil {
		t.Fatal(err)
	}
	socket := make(chan string, 1)
	integration := appServerIntegration{controlSocket: socket}
	codexHome := t.TempDir()
	provider := &toolFrontendCodexProvider{
		program:  `const result = await tools.exec_command({cmd:` + strconv.Quote(quoteShellWord(fakeSudo)+" id") + `,shell:"/bin/bash",login:false,yield_time_ms:30000}); text(result.output);`,
		expected: []string{"AUTHENTICATED"}, finalText: "Recovered after a retry.",
		observeRequest: func(body []byte, _ http.Header) error {
			if bytes.Contains(body, []byte(secret)) {
				return errors.New("password appeared in a model request")
			}
			return nil
		},
	}
	runAppServerPreviewWith(t, provider, nil, appServerPreview{
		managedRuntime: true, integration: &integration, environment: []string{"SHELL=/bin/bash", "CODEX_HOME=" + codexHome},
		beforePrompt: func(t *testing.T, _ io.Writer, _ func(string), _ func(func(string) bool), _ *vt.Emulator) {
			bridge, err := connector.New(connector.Options{SocketPath: <-socket, TokenFile: tokenFile, Origins: []string{page.URL}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = bridge.Close() })
			remote := httptest.NewServer(bridge)
			t.Cleanup(remote.Close)
			run("find", "label", "Connector URL", "fill", remote.URL)
			run("find", "role", "button", "click", "--name", "Connect", "--exact")
			run("click", "nav[aria-label=Threads] li a")
			run("wait", "#composer-text")
		},
		duringTurn: func(t *testing.T, outer io.Writer, await func(string), _ func(func(string) bool), _ *vt.Emulator) {
			await("Allow this sudo command?")
			run("wait", "--text", "Allow this sudo command?")
			run("find", "role", "button", "click", "--name", "Yes, run it", "--exact")
			await("Enter your sudo password:")
			run("wait", "--text", "Answer this secret prompt in the terminal.")
			run("wait", "--fn", `document.querySelector('[data-prompt]') && !document.querySelector('[data-prompt] input, [data-prompt] textarea')`)
			if _, err := io.WriteString(outer, secret+"\r"); err != nil {
				t.Fatal(err)
			}
		},
	})
	rollouts, err := filepath.Glob(filepath.Join(codexHome, "sessions", "*", "*", "*", "rollout-*.jsonl"))
	if err != nil || len(rollouts) == 0 {
		t.Fatalf("missing native rollout: %v", err)
	}
	for _, path := range rollouts {
		data, err := os.ReadFile(path)
		if err != nil || bytes.Contains(data, []byte(secret)) {
			t.Fatalf("invalid private rollout: %v", err)
		}
	}
}
