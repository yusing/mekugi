package launch

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/webui"
)

func TestWebUIBrowserParentInput(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_WEBUI_BROWSER") != "1" {
		t.Skip("set MEKUGI_TEST_WEBUI_BROWSER=1 after make webui")
	}
	t.Setenv("AGENT_BROWSER_ENGINE", "chrome")
	browser := os.Getenv("MEKUGI_TEST_AGENT_BROWSER")
	if browser == "" {
		browser = "agent-browser"
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	page := httptest.NewServer(webui.Handler())
	defer page.Close()
	machine := newWebUIBrowserMachine(t, ctx, page.URL, "Lifecycle", strings.Repeat("c", 64))
	u, _ := parentInputUI(t, "/root")
	publish := func() {
		t.Helper()
		if err := machine.publishFrame(sessionapi.Frame{Session: sessionapi.Session{Thread: "same-thread", Title: "Lifecycle thread", Caller: "/root/worker", Workspace: u.attached.cwd}, Input: &sessionapi.Input{}, Main: u.attached.source.Publication(u.attached.presentation, nil)}); err != nil {
			t.Fatal(err)
		}
	}
	publish()
	identity, err := exec.CommandContext(ctx, browser, "session", "id", "--scope", "worktree", "--prefix", "webui-parent-input").CombinedOutput()
	if err != nil {
		t.Fatalf("browser identity: %v %s", err, identity)
	}
	name := strings.TrimSpace(string(identity))
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = exec.CommandContext(cleanup, browser, "--session", name, "close").Run()
	}()
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, browser, append([]string{"--session", name}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("browser %s: %v %s", args[0], err, out)
		}
		return string(out)
	}
	run("open", page.URL)
	run("find", "label", "Machine name", "fill", "Lifecycle")
	run("find", "label", "Connector URL", "fill", machine.remote.URL)
	run("find", "label", "Access token", "fill", strings.Repeat("c", 64))
	run("find", "role", "button", "click", "--name", "Connect", "--exact")
	run("wait", "--text", "Lifecycle thread")
	run("find", "text", "Lifecycle thread", "click", "--exact")
	for _, width := range []int{1280, 768, 390} {
		run("set", "viewport", fmt.Sprint(width), "900")
		for _, theme := range []string{"light", "dark"} {
			run("set", "media", theme)
			run("wait", "--fn", `(() => { const rows = [...document.querySelectorAll('[data-ai-element="subagent"]')]; return ['Initial parent assignment.', 'Follow-up parent assignment.', 'Parent message.'].every(text => { const matches = rows.filter(row => row.textContent.includes(text)); return matches.length === 1 && matches[0].textContent.includes('main') && matches[0].textContent.includes('worker') && !matches[0].textContent.includes('You') && matches[0].getBoundingClientRect().right <= window.innerWidth; }); })()`)
			if directory := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); directory != "" {
				run("screenshot", filepath.Join(directory, fmt.Sprintf("parent-input-%s-%d.png", theme, width)))
			}
		}
	}
}
