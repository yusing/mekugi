package launch

import (
	"bufio"
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

// Wait rows and tool-managed edit summaries read as web text: aligned
// markers, linked agents beyond the loaded roster page and coloured counts.
func TestWebUIBrowserWaitTargets(t *testing.T) {
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
	machine := newWebUIBrowserMachine(t, ctx, page.URL, "Waits", strings.Repeat("d", 64))
	agents := &activity.Publication{}
	// More agents than one roster page holds, so later agents start unloaded.
	for i := range 120 {
		agents.Roster = append(agents.Roster, activity.AgentRow{Agent: activity.Agent{AgentFacts: activity.AgentFacts{Name: fmt.Sprintf("/root/worker%d", i), Final: true}}})
	}
	wait := activity.Value{Kind: "progress", Body: "Finished waiting for", WaitReason: "worker119: completed"}
	for _, target := range []string{"agent:main/worker0", "agent:main/worker1", "agent:main/worker118", "agent:main/worker119"} {
		wait.WaitTargets = append(wait.WaitTargets, activity.WaitTarget{Name: target})
	}
	main := &activity.Publication{Entries: []activity.Entry{{Facts: activity.Facts{Seq: 1, Agent: "Main", Kind: "tool", Text: wait.ProgressText()}, Values: []activity.Value{wait}, Revision: 1}}}
	for i, text := range []string{
		"+ 20 tool-managed files (diagnostic: +364 -53) · git cherry-pick",
		"+ 2 tool-managed files (diagnostic: counts unavailable) · git reset",
	} {
		edit := activity.ItemContent{Facts: activity.Facts{Seq: uint64(i + 2), Agent: "Main", Kind: "tool", Text: text}}
		main.Entries = append(main.Entries, activity.Entry{Facts: edit.Facts, Values: edit.Values(), Revision: 1})
	}
	frame := sessionapi.Frame{Session: sessionapi.Session{Thread: "same-thread", Title: "Waits thread", Caller: "/root", Workspace: machine.workdir}, Input: &sessionapi.Input{}, Main: main, Agents: agents}
	if err := machine.publishFrame(frame); err != nil {
		t.Fatal(err)
	}
	identity, err := exec.CommandContext(ctx, browser, "session", "id", "--scope", "worktree", "--prefix", "webui-wait").CombinedOutput()
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
			state, _ := exec.CommandContext(ctx, browser, "--session", name, "eval", `document.body.innerText.slice(0, 1500)`).CombinedOutput()
			t.Fatalf("browser %s: %v %s\n%s", args[0], err, out, state)
		}
		return string(out)
	}
	run("open", page.URL)
	run("set", "viewport", "1280", "900")
	run("find", "label", "Machine name", "fill", "Waits")
	run("find", "label", "Connector URL", "fill", machine.remote.URL)
	run("find", "label", "Access token", "fill", strings.Repeat("d", 64))
	run("find", "role", "button", "click", "--name", "Connect", "--exact")
	run("wait", "--text", "Waits thread")
	run("find", "text", "Waits thread", "click", "--exact")
	run("wait", "--text", "+2 more")
	for _, width := range []int{1280, 768, 390} {
		run("set", "viewport", fmt.Sprint(width), "900")
		for _, theme := range []string{"light", "dark"} {
			run("set", "media", theme)
			// The marker centres on the first text line, and the elision
			// control stays within that line. The reason is its own clause.
			run("wait", "--fn", `(() => { const row = document.querySelector('[data-ai-element="agent-progress"]'); const icon = row.querySelector('svg').getBoundingClientRect(); const range = document.createRange(); range.selectNodeContents(row.querySelector('span.min-w-0 > span')); const line = range.getClientRects()[0]; const button = [...row.querySelectorAll('button')].find(b => b.textContent === '+2 more'); range.selectNodeContents(button); const more = range.getBoundingClientRect(); return Math.abs((icon.top + icon.bottom) / 2 - (line.top + line.bottom) / 2) <= 1 && more.top >= line.top - 3 && more.bottom <= line.bottom + 3 && (innerWidth > 640 || button.getBoundingClientRect().height >= 44) && row.innerText.includes('+2 more ·\u00a0worker119: completed') && !row.innerText.includes('more :'); })()`)
			// Tool-managed counts read as coloured counts, without the
			// diagnostic wording, beside the command chip.
			run("wait", "--fn", `(() => { const row = n => document.querySelector('[data-entry="'+n+'"] [data-ai-element=tool]'); const done = row(2), unknown = row(3); return done.innerText.includes('20 tool-managed files +364 −53') && done.querySelector('.text-positive')?.textContent === '+364' && done.querySelector('.text-negative')?.textContent === '−53' && done.innerText.includes('git cherry-pick') && unknown.innerText.includes('counts unavailable') && !document.querySelector('[aria-label="Transcript"]').innerText.includes('diagnostic'); })()`)
			if directory := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); directory != "" {
				run("screenshot", filepath.Join(directory, fmt.Sprintf("wait-targets-%s-%d.png", theme, width)))
			}
		}
	}
	// Chromium's touch emulation changes the actual pointer media query;
	// viewport resizing alone does not exercise coarse-pointer hit regions.
	run("set", "viewport", "390", "900")
	touch := exec.CommandContext(ctx, "node", "--input-type=module", "-e", `
const socket = new WebSocket(process.argv[1]);
await new Promise(resolve => socket.addEventListener('open', resolve, {once:true}));
let id = 0; const pending = new Map();
socket.addEventListener('message', event => { const reply = JSON.parse(event.data); if (reply.id) { pending.get(reply.id)(reply); pending.delete(reply.id); } });
const call = (method, params = {}, sessionId) => new Promise((resolve, reject) => { pending.set(++id, reply => reply.error ? reject(new Error(reply.error.message)) : resolve(reply.result)); socket.send(JSON.stringify({id, method, params, sessionId})); });
const {targetInfos} = await call('Target.getTargets');
const {sessionId} = await call('Target.attachToTarget', {targetId:targetInfos.find(target => target.type === 'page').targetId, flatten:true});
await call('Emulation.setTouchEmulationEnabled', {enabled:true, maxTouchPoints:5}, sessionId);
console.log('ready');`, strings.TrimSpace(run("get", "cdp-url")))
	touch.Stderr = os.Stderr
	touchOutput, err := touch.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := touch.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = touch.Process.Kill()
		_ = touch.Wait()
	}()
	if _, err := bufio.NewReader(touchOutput).ReadString('\n'); err != nil {
		t.Fatalf("touch emulation: %v", err)
	}
	run("wait", "--fn", `matchMedia('(pointer:coarse)').matches && document.querySelector('[data-ai-element="agent-progress"] button[aria-expanded]').getBoundingClientRect().height >= 44`)
	run("set", "viewport", "1280", "900")
	// A newer publication supersedes the first targeted roster read. The
	// expanded name must become navigable without another publication or click.
	run("eval", `window.targetRosterReads = []; const send = WebSocket.prototype.send; WebSocket.prototype.send = function(text) { const request = JSON.parse(text); if (request.method === 'session/page' && request.params.body?.caller === '/root/worker119') { window.targetRosterReads.push(() => send.call(this, text)); return; } send.call(this, text); };`)
	run("find", "text", "+2 more", "click", "--exact")
	run("wait", "--fn", `window.targetRosterReads.length === 1`)
	run("wait", "--fn", `!document.querySelector('[data-ai-element="agent-progress"] button[title="Show worker119\'s activity"]')`)
	frame.Agents = &activity.Publication{Roster: agents.Roster}
	frame.Session.Title = "Wait roster revised"
	if err := machine.publishFrame(frame); err != nil {
		t.Fatal(err)
	}
	run("wait", "--text", "Wait roster revised")
	run("eval", `window.targetRosterReads[0]()`)
	run("wait", "--fn", `window.targetRosterReads.length === 2`)
	run("eval", `window.targetRosterReads[1]()`)
	// Real expanded targets become links once the lookup confirms membership.
	run("wait", "--fn", `(() => { const row = document.querySelector('[data-ai-element="agent-progress"]'); return ['worker0', 'worker1', 'worker118', 'worker119'].every(name => row.querySelector('button[title="Show ' + name + '\'s activity"]')); })()`)
	if directory := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); directory != "" {
		run("screenshot", filepath.Join(directory, "wait-targets-expanded.png"))
	}
	run("click", `[data-ai-element="agent-progress"] button[title="Show worker119's activity"]`)
	run("wait", "--fn", `!!document.querySelector('[aria-label="worker119\'s activity"]') && document.querySelector('#panel-content')?.textContent.includes('worker119')`)
}
