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
	activity "github.com/yusing/mekugi/modules/activity/presentation"
)

func TestWebUIBrowserRetainedHistory(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_WEBUI_BROWSER") != "1" {
		t.Skip("set MEKUGI_TEST_WEBUI_BROWSER=1 after make webui, with agent-browser installed")
	}
	t.Setenv("AGENT_BROWSER_ENGINE", "chrome")
	browser := os.Getenv("MEKUGI_TEST_AGENT_BROWSER")
	if browser == "" {
		browser = "agent-browser"
	}
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	page := httptest.NewServer(webui.Handler())
	defer page.Close()
	machine := newWebUIBrowserMachine(t, ctx, page.URL, "History", strings.Repeat("e", 64))
	main, agents := &activity.Publication{}, &activity.Publication{}
	for i := range 600 {
		text := fmt.Sprintf("History entry %03d\n\n**Retained evidence** with [a link](https://example.com).\n\n```ts\nconst history = %d;\n```", i, i)
		main.Entries = append(main.Entries, activity.Entry{Facts: activity.Facts{Seq: uint64(i + 1), Agent: "Main", Kind: "text", Text: text}, Revision: 1})
		agents.Entries = append(agents.Entries, activity.Entry{Facts: activity.Facts{Seq: uint64(i + 1), Agent: fmt.Sprintf("/root/worker%d", i%4), Kind: "text", Text: text}, Revision: 1})
	}
	for i := range 4 {
		agents.Roster = append(agents.Roster, activity.AgentRow{Agent: activity.Agent{AgentFacts: activity.AgentFacts{Name: fmt.Sprintf("/root/worker%d", i)}}})
	}
	frame := sessionapi.Frame{Session: sessionapi.Session{Thread: "same-thread", Title: "History thread", Workspace: machine.workdir}, Input: &sessionapi.Input{Stoppable: true}, Main: main, Agents: agents}
	start := time.Now()
	if err := machine.api.Publish(frame); err != nil {
		t.Fatal(err)
	}
	t.Log("publish 600 Main and 600 child entries:", time.Since(start))
	identity, err := exec.CommandContext(ctx, browser, "session", "id", "--scope", "worktree", "--prefix", "webui-history").CombinedOutput()
	if err != nil {
		t.Fatalf("browser identity: %v: %s", err, identity)
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
			t.Fatalf("browser %s: %v: %s", args[0], err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("open", page.URL)
	run("find", "label", "Machine name", "fill", "History")
	run("find", "label", "Connector URL", "fill", machine.remote.URL)
	run("find", "label", "Access token", "fill", strings.Repeat("e", 64))
	run("find", "role", "button", "click", "--name", "Connect", "--exact")
	run("wait", "--text", "History thread")
	t.Log("retained thread first paint:", run("eval", `new Promise(resolve => { const start = performance.now(); new MutationObserver(function (_, observer) { const transcript = document.querySelector('[aria-label="Transcript"]'); if (!transcript?.textContent.includes('History entry 599')) return; observer.disconnect(); requestAnimationFrame(() => requestAnimationFrame(() => resolve({ms: performance.now() - start, rows: transcript.querySelectorAll('article').length}))); }).observe(document.body, {childList: true, subtree: true}); [...document.querySelectorAll('a')].find(e => e.textContent.includes('History thread')).click(); })`))
	if run("eval", `document.querySelectorAll('[aria-label="Transcript"] article').length <= 50`) != "true" {
		t.Fatal("opening retained history rendered more than the initial row budget")
	}
	run("wait", "--fn", `document.querySelector('button[aria-label="Stop"]')?.disabled === false`)
	// Earlier pages preserve the same first visible row while prepending, and
	// new publications do not take away the history being read.
	run("eval", `(() => { const s = document.querySelector('[aria-label="Transcript"] .overflow-y-auto'); s.scrollTop = 0; window.historyAnchor = s.querySelector('[data-entry]'); window.historyTop = window.historyAnchor.getBoundingClientRect().top; [...s.querySelectorAll('button')].find(e => e.textContent.includes('earlier entries')).focus(); })()`)
	run("press", "Enter")
	run("wait", "--fn", `document.querySelectorAll('[aria-label="Transcript"] article').length === 100`)
	if run("eval", `Math.abs(window.historyAnchor.getBoundingClientRect().top - window.historyTop) < 2`) != "true" {
		t.Fatal("prepending earlier history moved the reading anchor")
	}
	if run("eval", `document.activeElement === window.historyAnchor && document.activeElement.getBoundingClientRect().top >= document.querySelector('[aria-label="Transcript"]').getBoundingClientRect().top`) != "true" {
		t.Fatal("loading earlier history left keyboard focus outside the visible reading anchor")
	}
	frame.Main = &activity.Publication{Entries: append(main.Entries, activity.Entry{Facts: activity.Facts{Seq: 601, Agent: "Main", Kind: "text", Text: "Newest retained update"}, Revision: 1})}
	if err := machine.api.Publish(frame); err != nil {
		t.Fatal(err)
	}
	run("wait", "--text", "Newest retained update")
	if run("eval", `document.querySelector('[aria-label="Transcript"]').textContent.includes('History entry 500')`) != "true" {
		t.Fatal("an incoming publication discarded history being read")
	}
	for range 10 {
		run("eval", `[...document.querySelectorAll('[aria-label="Transcript"] button')].find(e => e.textContent.includes('earlier entries'))?.click()`)
	}
	run("wait", "--text", "History entry 000")
	if run("eval", `document.querySelectorAll('[aria-label="Transcript"] article').length === 601`) != "true" {
		t.Fatal("earlier pages did not retain all available history")
	}
	run("find", "role", "button", "click", "--name", "Latest", "--exact")
	run("wait", "--fn", `document.querySelectorAll('[aria-label="Transcript"] article').length === 50`)
	// A different agent starts at its own newest page, without Main's cursor.
	run("eval", `[...document.querySelectorAll('[aria-label="Thread panel"] button')].find(e => e.textContent.includes('worker0')).click()`)
	run("wait", "--fn", `document.querySelectorAll('[aria-label="worker0\'s activity"] article').length === 50`)
	if run("eval", `document.querySelector('[aria-label="worker0\'s activity"]').textContent.includes('History entry 596')`) != "true" {
		t.Fatal("agent selection did not show that agent's latest retained entry")
	}
	if directory := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); directory != "" {
		for _, size := range []struct{ name, width, height string }{{"desktop", "1280", "900"}, {"phone", "390", "844"}} {
			run("set", "viewport", size.width, size.height)
			run("eval", `document.querySelector('[aria-label="worker0\'s activity"] .overflow-y-auto').scrollTop = 0`)
			run("screenshot", filepath.Join(directory, "history-"+size.name+".png"))
		}
	}
}
