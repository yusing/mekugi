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
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/webui"
	"github.com/yusing/mekugi/contracts/presentation"
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
	type attachedView struct {
		native  *sessionapi.Native
		request sessionapi.NativeRequest
	}
	attachments := make(chan attachedView, 4)
	resumePages := make(chan sessionpresentation.ResumeAction, 4)
	machine := newWebUIBrowserMachineWithNative(t, ctx, page.URL, "History", strings.Repeat("e", 64), func(native *sessionapi.Native, request sessionapi.NativeRequest) bool {
		if request.Attach {
			attachments <- attachedView{native, request}
		}
		if request.Operation.Resume != nil {
			resumePages <- *request.Operation.Resume
			request.Reply <- ""
			return true
		}
		return false
	})
	main, agents := &activity.Publication{}, &activity.Publication{}
	for i := range 600 {
		text := fmt.Sprintf("History entry %03d\n\n**Retained evidence** with [a link](https://example.com).\n\n```ts\nconst history = %d;\n```", i, i)
		if i%50 == 49 {
			text += strings.Repeat("\n\nRetained content acquired while scrolling.", 80)
		}
		main.Entries = append(main.Entries, activity.Entry{Seq: uint64(i + 1), Agent: "Main", Kind: "text", Text: text, Revision: 1})
		agents.Entries = append(agents.Entries, activity.Entry{Seq: uint64(i + 1), Agent: fmt.Sprintf("/root/worker%d", i%4), Kind: "text", Text: text, Revision: 1})
	}
	for i := range 120 {
		agents.Roster = append(agents.Roster, activity.AgentRow{Agent: activity.Agent{AgentFacts: activity.AgentFacts{Name: fmt.Sprintf("/root/worker%d", i), Responding: i < 2, Final: i >= 2}}})
	}
	frame := sessionapi.Frame{Session: sessionapi.Session{Thread: "same-thread", Title: "History thread", Workspace: machine.workdir}, Input: &sessionapi.Input{Stoppable: true}, Main: main, Agents: agents}
	start := time.Now()
	if err := machine.publishFrame(frame); err != nil {
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
			state, _ := exec.CommandContext(ctx, browser, "--session", name, "eval", `(() => { const pane = document.querySelector('#panel-content > div'); return {text:pane?.textContent.slice(-1200), rows:pane?.querySelectorAll('li > button').length, height:pane?.clientHeight, scroll:pane?.scrollTop, total:pane?.scrollHeight, end:pane?.lastElementChild?.getBoundingClientRect().toJSON()}; })()`).CombinedOutput()
			t.Fatalf("browser %s: %v: %s\n%s", args[0], err, out, state)
		}
		return strings.TrimSpace(string(out))
	}
	run("open", page.URL)
	run("eval", `window.controlSockets = []; window.contentReads = 0; const Original = WebSocket; window.WebSocket = class extends Original { constructor(...args) { super(...args); window.controlSockets.push(this); } }; const originalFetch = window.fetch; window.fetch = (...args) => { if (String(args[0]).endsWith("/api/resources/content")) window.contentReads++; return originalFetch(...args); }`)
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
	if run("eval", `(() => {
		const header = document.querySelector('#panel-content h2').parentElement;
		const showing = header.children[1], counts = header.children[2];
		const match = showing.textContent.match(/Showing\s+(\d+)\s+of\s+(\d+)/);
		if (!match) throw Error('Unexpected roster header: ' + header.textContent);
		const loaded = Number(match[1]);
		window.rosterTotal = Number(match[2]);
		return [...counts.childNodes].every(node => node.nodeType !== Node.TEXT_NODE || !node.textContent.trim()) &&
			counts.children[0].textContent === '2 working' && counts.children[1].textContent === (loaded - 2) + ' done';
	})()`) != "true" {
		t.Fatal("partial roster duplicated its loaded count or lost state counts")
	}
	for _, palette := range []string{"light", "dark"} {
		run("eval", fmt.Sprintf(`document.documentElement.dataset.theme = %q`, palette))
		for _, width := range []string{"1280", "768", "390"} {
			run("set", "viewport", width, "900")
			run("eval", `if (!document.querySelector('#panel-content')) document.querySelector('button[aria-label^="Agents ("]').click()`)
			run("wait", "--fn", `!!document.querySelector('#panel-content h2')`)
			if run("eval", `(() => {
				const header = document.querySelector('#panel-content h2').parentElement, bounds = header.getBoundingClientRect();
				return [header.children[1], ...header.children[2].children].every(label => {
					const range = document.createRange(); range.selectNodeContents(label);
					const rects = [...range.getClientRects()];
					return rects.length === 1 && rects[0].left >= bounds.left - 1 && rects[0].right <= bounds.right + 1;
				});
			})()`) != "true" {
				t.Fatalf("roster count labels wrapped internally or overflowed at %s/%s", palette, width)
			}
			if directory := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); directory != "" {
				run("screenshot", filepath.Join(directory, "roster-count-"+palette+"-"+width+".png"))
			}
		}
	}
	run("set", "viewport", "1280", "900")
	// The roster acquires bounded pages at its scroll edge without a loading control.
	run("eval", `window.rosterFirst = document.querySelector('#panel-content li > button')`)
	for range 5 {
		if run("eval", `document.querySelector('#panel-content')?.textContent.includes('worker119')`) == "true" {
			break
		}
		run("eval", `(() => { const list = document.querySelector('#panel-content > div'); window.rosterCount = list.querySelectorAll('li > button').length; list.scrollTop = list.scrollHeight; })()`)
		run("wait", "--fn", `document.querySelector('#panel-content').querySelectorAll('li > button').length > window.rosterCount`)
	}
	if run("eval", `window.rosterFirst.isConnected && document.querySelector('#panel-content').textContent.includes('worker119') && !document.body.innerText.includes('Load more')`) != "true" {
		t.Fatal("roster paging lost rows, selection, or automatic demand")
	}
	if run("eval", `(() => { const header = document.querySelector('#panel-content h2').parentElement; return !header.textContent.includes('Showing') && [...header.lastElementChild.childNodes].some(node => node.nodeType === Node.TEXT_NODE && node.textContent.trim() === String(window.rosterTotal)); })()`) != "true" {
		t.Fatal("complete mixed-state roster lost its total count")
	}
	// A new bounded first page updates activity without hiding acquired members.
	run("eval", `window.lastMember = [...document.querySelectorAll('#panel-content li > button')].find(row => row.textContent.includes('worker119'))`)
	for i := range 3 {
		updated := *agents
		updated.Roster = append([]activity.AgentRow(nil), agents.Roster...)
		updated.Roster[0].Agent.StatusItems = []presentation.Item{{Text: fmt.Sprintf("Roster update %d", i)}}
		frame.Agents = &updated
		if err := machine.publishFrame(frame); err != nil {
			t.Fatal(err)
		}
		run("wait", "--text", fmt.Sprintf("Roster update %d", i))
		if run("eval", `window.lastMember.isConnected && !document.body.innerText.includes('The roster changed')`) != "true" {
			t.Fatal("roster activity churn removed an acquired member or required manual recovery")
		}
	}
	run("eval", `document.querySelector('#panel-content > div').scrollTop = 0`)
	// Following's 80px hysteresis must not prevent paging a near-fitting feed.
	run("eval", `(() => { const s = document.querySelector('[aria-label="Transcript"] .overflow-y-auto'); window.originalHistoryStyle = s.getAttribute('style') ?? ''; const height = s.scrollHeight - 40; s.style.height = s.style.minHeight = s.style.maxHeight = height + 'px'; s.scrollTop = 40; s.dispatchEvent(new WheelEvent('wheel', {bubbles:true})); window.nearFitRows = s.querySelectorAll('article').length; if (s.scrollHeight - s.clientHeight < 1 || s.scrollHeight - s.clientHeight >= 80) throw Error('History fixture must have less than 80px overflow'); s.scrollTop = 0; })()`)
	run("wait", "--fn", `document.querySelectorAll('[aria-label="Transcript"] article').length > window.nearFitRows`)
	run("eval", `document.querySelector('[aria-label="Transcript"] .overflow-y-auto').setAttribute('style', window.originalHistoryStyle)`)
	// Earlier pages preserve the same first visible row while prepending, and
	// new publications do not take away the history being read.
	run("eval", `(() => { const s = document.querySelector('[aria-label="Transcript"] .overflow-y-auto'); s.dispatchEvent(new WheelEvent('wheel', {bubbles:true})); s.scrollTop = s.clientHeight / 2; window.historyAnchor = s.querySelector('[data-entry]'); window.historyTop = window.historyAnchor.getBoundingClientRect().top; window.historyFocus = document.activeElement; window.historyRows = s.querySelectorAll("article").length; window.historyShifted = false; window.historyObserver = new MutationObserver(() => { if (s.textContent.includes("Loading earlier entries")) window.historyShifted = true; }); window.historyObserver.observe(s, {childList:true, subtree:true}); })()`)
	run("wait", "--fn", `document.querySelectorAll('[aria-label="Transcript"] article').length > window.historyRows`)
	if run("eval", `Math.abs(window.historyAnchor.getBoundingClientRect().top - window.historyTop) < 2`) != "true" {
		t.Fatal("preloading history before the top moved the reading anchor")
	}
	if run("eval", `document.activeElement === window.historyFocus`) != "true" {
		t.Fatal("automatic history paging moved keyboard focus")
	}
	// Hold one receipt so subsequent updates must coalesce to a snapshot.
	run("eval", `window.savedSend = WebSocket.prototype.send; WebSocket.prototype.send = function(text) { if (JSON.parse(text).method === 'publication/ack') { window.heldReceipt = () => window.savedSend.call(this, text); return; } return window.savedSend.call(this, text); }`)
	frame.Main = &activity.Publication{Entries: append(main.Entries, activity.Entry{Seq: 601, Agent: "Main", Kind: "text", Text: "Newest retained update", Revision: 1})}
	if err := machine.publishFrame(frame); err != nil {
		t.Fatal(err)
	}
	run("wait", "--text", "Newest retained update")
	run("wait", "--fn", `!!window.heldReceipt`)
	for i := range 10 {
		frame.Session.Title = fmt.Sprintf("History catch-up %d", i)
		if err := machine.publishFrame(frame); err != nil {
			t.Fatal(err)
		}
	}
	run("eval", `WebSocket.prototype.send = window.savedSend; window.heldReceipt()`)
	run("wait", "--text", "History catch-up 9")
	if run("eval", `document.querySelector('[aria-label="Transcript"]').textContent.includes('History entry 550')`) != "true" {
		t.Fatal("a coalesced publication discarded history being read")
	}
	if run("eval", `document.activeElement === window.historyFocus && Math.abs(window.historyAnchor.getBoundingClientRect().top - window.historyTop) < 2`) != "true" {
		t.Fatal("catch-up interrupted the reading anchor or focus")
	}
	for range 15 {
		if run("eval", `document.querySelector('[aria-label="Transcript"]').textContent.includes('History entry 000')`) == "true" {
			break
		}
		run("eval", `(() => { const s = document.querySelector('[aria-label="Transcript"] .overflow-y-auto'); window.historyRows = s.querySelectorAll('article').length; s.scrollTop = 0; })()`)
		run("wait", "--fn", `document.querySelectorAll('[aria-label="Transcript"] article').length > window.historyRows`)
	}
	run("wait", "--text", "History entry 000")
	run("eval", `window.historyObserver.disconnect(); if (window.historyShifted) throw Error("History loading inserted an in-flow row")`)
	run("wait", "--fn", `window.contentReads >= 12`)
	if counts := run("eval", `({created:window.controlSockets.length,open:window.controlSockets.filter(socket => socket.readyState === WebSocket.OPEN).length,content:window.contentReads})`); run("eval", `window.controlSockets.length === 1 && window.controlSockets.every(socket => socket.readyState === WebSocket.OPEN)`) != "true" {
		t.Fatalf("scrolling created additional control connections: %s", counts)
	}
	if run("eval", `document.querySelectorAll('[aria-label="Transcript"] article').length === 601`) != "true" {
		t.Fatal("earlier pages did not retain all available history")
	}
	run("find", "role", "button", "click", "--name", "Latest", "--exact")
	run("wait", "--fn", `document.querySelectorAll('[aria-label="Transcript"] article').length === 50`)
	// An unrelated native frame must preserve an in-flight complete-detail read.
	var attachment attachedView
	select {
	case attachment = <-attachments:
	case <-ctx.Done():
		t.Fatal("missing live view")
	}
	run("eval", `window.detailReads = []; window.savedFetch = window.fetch; window.fetch = (...args) => { if (String(args[0]).endsWith('/api/resources/content')) return new Promise((resolve, reject) => window.detailReads.push(() => window.savedFetch(...args).then(resolve, reject))); return window.savedFetch(...args); }`)
	thread := sessionpresentation.Thread{ID: "same-thread", Caller: "/root"}
	detailFrame := sessionapi.NativeFrame{View: sessionpresentation.View{Version: sessionpresentation.Version{Attachment: 1, Revision: 2}, Main: thread, Input: thread, Displayed: thread}, Details: &sessionapi.NativeDetails{Source: thread, View: sessionpresentation.DetailView{ID: sessionpresentation.DetailID{Attachment: 1, Selection: 1}, Revision: 1, Pages: []sessionpresentation.DetailItem{{ID: "retained", Body: strings.Repeat("Complete retained details.\n\n", 120)}}}}}
	detailFrame.View.Info.Workspace = machine.workdir
	if err := attachment.native.Publish(attachment.request.Attachment, detailFrame); err != nil {
		t.Fatal(err)
	}
	run("wait", "--fn", `window.detailReads.length === 1`)
	detailFrame.View.Version.Revision++
	if err := attachment.native.Publish(attachment.request.Attachment, detailFrame); err != nil {
		t.Fatal(err)
	}
	run("eval", `new Promise(resolve => setTimeout(resolve, 300))`)
	if run("eval", `window.detailReads.length === 1`) != "true" {
		t.Fatal("unrelated frame restarted the complete-detail read")
	}
	run("eval", `window.fetch = window.savedFetch; window.detailReads[0]()`)
	run("wait", "--text", "Complete retained details.")
	if run("eval", `window.controlSockets.length === 1`) != "true" {
		t.Fatal("opening retained details created another control connection")
	}
	run("eval", `window.savedDetails = document.querySelector('#details-heading').parentElement.nextElementSibling.querySelector('p')`)
	detailFrame.View.Version.Revision++
	if err := attachment.native.Publish(attachment.request.Attachment, detailFrame); err != nil {
		t.Fatal(err)
	}
	run("eval", `new Promise(resolve => setTimeout(resolve, 300))`)
	if run("eval", `window.savedDetails.isConnected`) != "true" {
		t.Fatal("unrelated frame discarded hydrated details")
	}
	detailFrame.Details = nil
	detailFrame.View.Version.Revision++
	if err := attachment.native.Publish(attachment.request.Attachment, detailFrame); err != nil {
		t.Fatal(err)
	}
	run("wait", "--fn", `!document.querySelector('#details-heading')`)
	// A paged dialog appends to the existing keyed rows at its scroll edge.
	detailFrame.View.Resume = &sessionpresentation.ResumeView{ID: 7, CanPage: true, PageAfter: "saved-39"}
	for i := range 40 {
		detailFrame.View.Resume.Rows = append(detailFrame.View.Resume.Rows, sessionpresentation.ResumeRow{ID: fmt.Sprintf("saved-%d", i), Title: fmt.Sprintf("Saved row %d", i)})
	}
	detailFrame.View.Version.Revision++
	if err := attachment.native.Publish(attachment.request.Attachment, detailFrame); err != nil {
		t.Fatal(err)
	}
	run("wait", "--text", "Resume saved thread")
	run("eval", `window.resumeFirst = document.querySelector('[role=dialog][data-slot=dialog-content][data-state=open] li'); const dialog = document.querySelector('[role=dialog][data-slot=dialog-content][data-state=open]'); dialog.scrollTop = dialog.scrollHeight; window.resumeTop = dialog.scrollTop`)
	select {
	case action := <-resumePages:
		if action.Kind != sessionpresentation.ResumePage || action.After != "saved-39" {
			t.Fatal("automatic dialog page changed selection")
		}
	case <-ctx.Done():
		t.Fatal("dialog did not request its next page")
	}
	detailFrame.View.Resume = &sessionpresentation.ResumeView{ID: 7, Rows: append([]sessionpresentation.ResumeRow(nil), detailFrame.View.Resume.Rows...)}
	detailFrame.View.Resume.Rows = append(detailFrame.View.Resume.Rows, sessionpresentation.ResumeRow{ID: "saved-40", Title: "Automatically acquired saved row"})
	detailFrame.View.Version.Revision++
	if err := attachment.native.Publish(attachment.request.Attachment, detailFrame); err != nil {
		t.Fatal(err)
	}
	run("wait", "--text", "Automatically acquired saved row")
	if run("eval", `window.resumeFirst.isConnected && document.querySelector('[role=dialog][data-slot=dialog-content][data-state=open]').scrollTop >= window.resumeTop - 1 && !document.querySelector('[role=dialog][data-slot=dialog-content][data-state=open]').textContent.includes('Load more')`) != "true" {
		t.Fatal("dialog paging changed the reading position")
	}
	detailFrame.View.Resume = nil
	detailFrame.View.Version.Revision++
	if err := attachment.native.Publish(attachment.request.Attachment, detailFrame); err != nil {
		t.Fatal(err)
	}
	run("wait", "--fn", `!document.querySelector('[role=dialog][data-slot=dialog-content][data-state=open]')`)
	// Closing the actual wire must replace it once and restore all observers.
	run("eval", `window.controlSockets[0].close()`)
	run("wait", "--fn", `window.controlSockets.length === 2 && document.querySelector('button[aria-label="Stop"]')?.disabled === false`)
	if run("eval", `window.controlSockets.filter(socket => socket.readyState === WebSocket.OPEN).length === 1`) != "true" {
		t.Fatal("reconnection did not restore the control connection")
	}
	// A different agent starts at its own newest page, without Main's cursor.
	run("eval", `[...document.querySelectorAll('[aria-label="Thread panel"] button')].find(e => e.textContent.includes('worker0')).click()`)
	run("wait", "--fn", `(() => { const count = document.querySelectorAll('[aria-label="worker0\'s activity"] article').length; return count > 13 && count <= 50; })()`)
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

	if run("eval", `window.controlSockets.length === 2`) != "true" {
		t.Fatal("agent selection created another connection")
	}
	t.Log("scrolling and retained details reused one control connection and independent HTTP reads; disconnect replaced control; agent selection reused it")
}
