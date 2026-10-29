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
	"github.com/yusing/mekugi/backends/codex/binding"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
)

func TestWebUIBrowserCatalogAndInspect(t *testing.T) {
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
	machine := newWebUIBrowserMachine(t, ctx, page.URL, "Catalog", strings.Repeat("d", 64))
	u := newAppServerSessionTestUI(t, t.TempDir())
	var ghost binding.ThreadItem
	for _, item := range catalogInspectItems(t) {
		if item.ID == "inspect" {
			ghost = item
		}
	}
	ghost.ID, ghost.Result = "ghost-inspect", []byte(`{"content":[{"type":"text","text":"{\"thread_id\":\"ghost-thread\",\"task_name\":\"/root/ghost\"}"}]}`)
	items := append([]binding.ThreadItem{spawnActivityItem("completed")}, catalogInspectItems(t)...)
	items = append(items, ghost)
	for _, item := range items {
		phase := "item/completed"
		if item.Status == "inProgress" {
			phase = "item/started"
		}
		appServerTestNotify(t, u, phase, map[string]any{"threadId": "main", "turnId": "turn", "item": item})
	}
	agents := &activity.Publication{
		Entries: []activity.Entry{{Facts: activity.Facts{Seq: 1, Agent: "/root/worker", Kind: "text", Text: "Worker detail marker"}, Revision: 1}},
	}
	// Both inspected names start outside the first roster page. Only worker
	// exists, so pagination must not make ghost a navigable agent.
	for i := range 80 {
		agents.Roster = append(agents.Roster, activity.AgentRow{Agent: activity.Agent{AgentFacts: activity.AgentFacts{Name: fmt.Sprintf("/root/filler%d", i), Final: true}}})
	}
	agents.Roster = append(agents.Roster, activity.AgentRow{Agent: activity.Agent{AgentFacts: activity.AgentFacts{Name: "/root/worker", Responding: true}}})
	if err := machine.publishFrame(sessionapi.Frame{Session: sessionapi.Session{Thread: "same-thread", Title: "Catalog thread", Caller: "/root", Workspace: u.attached.cwd}, Input: &sessionapi.Input{}, Main: u.attached.source.Publication(u.attached.presentation, nil), Agents: agents}); err != nil {
		t.Fatal(err)
	}
	identity, err := exec.CommandContext(ctx, browser, "session", "id", "--scope", "worktree", "--prefix", "webui-catalog").CombinedOutput()
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
	run("eval", `window.ghostWasLinked = false; window.ghostReadCount = 0; new MutationObserver(() => { if (document.querySelector('button[title="Show ghost\'s activity"]')) window.ghostWasLinked = true; }).observe(document.body, {childList:true, subtree:true}); const send = WebSocket.prototype.send, listen = WebSocket.prototype.addEventListener; WebSocket.prototype.send = function(text) { const request = JSON.parse(text); if (request.method === 'session/page' && request.params.body?.caller === '/root/ghost') { window.ghostReadCount++; window.ghostRead = {id:request.id, release:() => send.call(this, text)}; return; } send.call(this, text); }; WebSocket.prototype.addEventListener = function(type, listener, ...options) { if (type !== 'message') return listen.call(this, type, listener, ...options); return listen.call(this, type, event => { if (window.ghostRead && JSON.parse(event.data).id === window.ghostRead.id) window.ghostLookupSettled = true; listener.call(this, event); }, ...options); };`)
	run("find", "label", "Machine name", "fill", "Catalog")
	run("find", "label", "Connector URL", "fill", machine.remote.URL)
	run("find", "label", "Access token", "fill", strings.Repeat("d", 64))
	run("find", "role", "button", "click", "--name", "Connect", "--exact")
	run("wait", "--text", "Catalog thread")
	run("find", "text", "Catalog thread", "click", "--exact")
	run("wait", "--text", "Listed 2 models")
	run("wait", "--fn", `!!window.ghostRead && !window.ghostWasLinked && !document.querySelector('button[title="Show ghost\'s activity"]')`)
	run("eval", `window.ghostRead.release()`)
	run("wait", "--fn", `window.ghostLookupSettled && window.ghostReadCount === 1 && !window.ghostWasLinked && document.querySelector('#panel-content')?.textContent.includes('Showing')`)
	// An admitted agent is a navigable name; an unknown one stays plain text.
	run("wait", "--fn", `(() => { const rows = [...document.querySelectorAll('[data-ai-element="agent-progress"]')]; const known = rows.find(r => r.textContent.trim() === 'Inspected worker'); const unknown = rows.find(r => r.textContent.trim() === 'Inspected ghost'); return known?.querySelector('button[title="Show worker\'s activity"]') && unknown && !unknown.querySelector('button'); })()`)
	for _, width := range []int{1280, 390} {
		run("set", "viewport", fmt.Sprint(width), "900")
		for _, theme := range []string{"light", "dark"} {
			run("set", "media", theme)
			run("wait", "--fn", `(() => { const rows = [...document.querySelectorAll('[data-ai-element="agent-progress"]')]; return ['Listed 2 models', 'Listed 1 model', 'Listed 0 models', 'Inspected', 'worker'].every(text => rows.some(row => row.textContent.includes(text) && !row.closest('[data-slot="card"]') && row.getBoundingClientRect().right <= window.innerWidth)) && !/isError|"content"|threadcontrol/.test(document.body.textContent); })()`)
			if directory := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); directory != "" {
				run("screenshot", filepath.Join(directory, fmt.Sprintf("catalog-inspect-%s-%d.png", theme, width)))
			}
		}
	}
	run("set", "viewport", "1280", "900")
	run("find", "role", "button", "click", "--name", "worker")
	run("wait", "--fn", `document.body.textContent.includes('Worker detail marker') && !document.body.textContent.includes('Listed 2 models')`)
}
