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
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestWebUIBrowserInterruptedTraffic(t *testing.T) {
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
	f := interruptedTrafficUI(t, false)
	f.observe(t, f.request(t, "interrupt"))
	u := f.ui
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": coordinationActivityItems(t)[5]})
	publish := func() {
		t.Helper()
		if err := machine.publishFrame(sessionapi.Frame{Session: sessionapi.Session{Thread: "same-thread", Title: "Lifecycle thread", Caller: "/root", Workspace: u.attached.cwd}, Input: &sessionapi.Input{}, Main: u.attached.source.Publication(u.attached.presentation, nil)}); err != nil {
			t.Fatal(err)
		}
	}
	publish()
	identity, err := exec.CommandContext(ctx, browser, "session", "id", "--scope", "worktree", "--prefix", "webui-interrupted").CombinedOutput()
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
	run("wait", "--text", "Interrupt requested for /root/worker")
	run("wait", "--text", "Listed 2 agents")
	f.complete(t)
	publishNativeDelivery(&u.attached, control.NativeInput{Thread: "main", ID: "interrupted", Sender: "/root/worker", Recipient: "/root", Kind: "FINAL_ANSWER", Outcome: control.Interrupted, Message: "Status: interrupted\n"}, u.clock())
	publish()
	for _, width := range []int{1280, 768, 390} {
		run("set", "viewport", fmt.Sprint(width), "900")
		for _, theme := range []string{"light", "dark"} {
			run("set", "media", theme)
			run("wait", "--fn", `document.body.textContent.includes('Interrupted /root/worker') && !document.body.textContent.includes('Interrupt requested') && [...document.querySelectorAll('.border-l-2')].some(e=>e.textContent.includes('worker') && e.textContent.includes('interrupted') && e.textContent.includes('■') && !e.textContent.includes('finished') && !e.querySelector('.text-positive') && e.getBoundingClientRect().right <= window.innerWidth)`)
			run("wait", "--fn", `(() => { const rows = [...document.querySelectorAll('[data-ai-element="agent-progress"]')]; return ['Interrupted /root/worker', 'Listed 2 agents'].every(text => rows.some(row => row.textContent.includes(text) && !row.closest('[data-slot="card"]') && row.querySelector('svg') && row.getBoundingClientRect().right <= window.innerWidth)); })()`)
			if directory := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); directory != "" {
				run("screenshot", filepath.Join(directory, fmt.Sprintf("interrupted-traffic-%s-%d.png", theme, width)))
			}
		}
	}
}
