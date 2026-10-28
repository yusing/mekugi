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
	"github.com/yusing/mekugi/contracts/observation"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
)

func TestWebUIBrowserDisclosures(t *testing.T) {
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
	attachments := make(chan sessionapi.NativeRequest, 1)
	machine := newWebUIBrowserMachineWithNative(t, ctx, page.URL, "Disclosure", strings.Repeat("f", 64), func(_ *sessionapi.Native, request sessionapi.NativeRequest) bool {
		if request.Attach {
			attachments <- request
		}
		return false
	})
	u := newAppServerSessionTestUI(t, t.TempDir())
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": coordinationActivityItems(t)[4]})
	publication := u.attached.source.Publication(u.attached.presentation, nil)
	for i, content := range []activity.ItemContent{
		{Facts: activity.Facts{Kind: "tool"}, Command: activity.Command{UserShell: true, Display: "printf complete", Tail: []string{"complete"}}},
		{Facts: activity.Facts{Kind: "tool"}, Command: activity.Command{UserShell: true, Display: "printf two", Tail: []string{"first output", "second output"}}},
		{Facts: activity.Facts{Kind: "command"}, Details: &activity.ItemDetails{CommandText: "printf first\nprintf second"}},
		{Facts: activity.Facts{Kind: "reply", Agent: "/root/worker"}, Message: &activity.DirectedContent{From: "/root/worker", To: "/root", Text: "Complete short reply."}},
		{Facts: activity.Facts{Kind: "reply", Agent: "/root/worker"}, Message: &activity.DirectedContent{From: "/root/worker", To: "/root", Text: strings.Repeat("Long reply paragraph.\n\n", 14)}},
		{Facts: activity.Facts{Kind: "tool"}, Command: activity.Command{UserShell: true, Display: "printf clipped", Tail: []string{"bounded…"}, TailClipped: true}},
		{Facts: activity.Facts{Kind: "error", Agent: "/root/worker", Text: "Complete short failure."}},
		{Facts: activity.Facts{Kind: "hook", Text: "Hook complete"}, Details: &activity.ItemDetails{Hook: &activity.HookDetails{Status: "completed"}}},
		{Facts: activity.Facts{Kind: "hook", Text: "Hook output"}, Details: &activity.ItemDetails{Hook: &activity.HookDetails{Status: "completed"}}, Command: activity.Command{Tail: []string{"Hidden hook output"}}},
		{Facts: activity.Facts{Kind: "hook", Text: "Hook failure"}, Details: &activity.ItemDetails{Hook: &activity.HookDetails{Status: "failed"}}, Command: activity.Command{Tail: []string{"Visible hook failure"}}},
		{Facts: activity.Facts{Kind: "hook", Text: strings.Repeat("Long hook label ", 20)}, Details: &activity.ItemDetails{Hook: &activity.HookDetails{Status: "completed"}}},
		{Facts: activity.Facts{Kind: "reasoning", Text: "**Checking callers**\n\nFirst thought body."}},
		{Facts: activity.Facts{Kind: "reasoning", Text: "**Choosing a fix**\n\nSecond thought body."}},
	} {
		content.Seq = uint64(i + 2)
		if content.Kind == "tool" {
			content.Text = content.Command.Display
		}
		if content.Agent == "" {
			content.Agent = "Main"
		}
		publication.Entries = append(publication.Entries, activity.Entry{Facts: content.Facts, Values: content.Values()})
	}
	mcp := activity.ItemContent{Facts: activity.Facts{Seq: 15, Agent: "Main", Kind: "tool", Text: activity.MCPText(observation.Item{Server: "plane", Tool: "workitem_relation", Status: "failed"})}}
	publication.Entries = append(publication.Entries, activity.Entry{Facts: mcp.Facts, Values: mcp.Values()})
	if err := machine.publishFrame(sessionapi.Frame{Session: sessionapi.Session{Thread: "same-thread", Title: "Disclosure thread", Workspace: machine.workdir}, Input: &sessionapi.Input{Stoppable: true}, Main: publication}); err != nil {
		t.Fatal(err)
	}
	identity, err := exec.CommandContext(ctx, browser, "session", "id", "--scope", "worktree", "--prefix", "webui-disclosure").CombinedOutput()
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
			state, _ := exec.CommandContext(ctx, browser, "--session", name, "eval", `JSON.stringify({rows:Array.from(document.querySelectorAll('[data-entry]')).map(row => ({seq: row.dataset.entry, text: row.innerText.slice(0,100), controls: Array.from(row.querySelectorAll('summary,button')).map(e => e.getAttribute('aria-label') || e.textContent)})), processes:document.querySelector('[aria-label="Active processes"]')?.innerText})`).CombinedOutput()
			t.Fatalf("browser %s: %v: %s\n%s", args[0], err, out, state)
		}
		return strings.TrimSpace(string(out))
	}
	run("open", page.URL)
	run("find", "label", "Machine name", "fill", "Disclosure")
	run("find", "label", "Connector URL", "fill", machine.remote.URL)
	run("find", "label", "Access token", "fill", strings.Repeat("f", 64))
	run("find", "role", "button", "click", "--name", "Connect", "--exact")
	run("find", "text", "Disclosure thread", "click", "--exact")
	run("wait", "--text", "Interrupt requested for /root/worker")
	for _, width := range []string{"1280", "768", "390"} {
		run("set", "viewport", width, "900")
		for _, theme := range []string{"light", "dark"} {
			run("eval", fmt.Sprintf("document.documentElement.dataset.theme = %q", theme))
			run("eval", `document.querySelector('[data-entry="1"]').closest('.overflow-y-auto').scrollTop = 0`)
			run("wait", "--fn", `(() => { const row = n => document.querySelector('[data-entry="'+n+'"]'); return [1,2,5].every(n => row(n) && !row(n).querySelector('button[aria-label="Details"], summary, button[aria-expanded]')) && row(1).innerText.includes('Interrupt requested for /root/worker') && row(2).innerText.includes('complete') && row(5).innerText.includes('Complete short reply.') && row(3).querySelector('button[aria-label="Output: 2 lines"]') && row(4).querySelector('button[aria-label="Script: 2 lines"]') && row(6).querySelector('button[aria-expanded]') && row(7).querySelector('button[aria-label="Details"]'); })()`)
			run("wait", "--fn", `(() => { const row = n => document.querySelector('[data-entry="'+n+'"]'); return [8,9,11].every(n => row(n) && !row(n).querySelector('button[aria-label="Details"]')) && row(8).innerText.includes('Complete short failure.') && row(11).innerText.includes('Visible hook failure') && [10,12].every(n => row(n)?.querySelector('button[aria-label="Details"]')); })()`)
			if artifacts := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); artifacts != "" {
				run("screenshot", filepath.Join(artifacts, "disclosures-"+width+"-"+theme+".png"))
				run("eval", `document.querySelector('[data-entry="15"]').scrollIntoView({block:'end'})`)
				run("screenshot", filepath.Join(artifacts, "hook-disclosures-"+width+"-"+theme+".png"))
			}
		}
	}
	// A failed MCP call states its outcome where a failed command does.
	run("wait", "--fn", `(() => { const row = document.querySelector('[data-entry="15"]'); return row?.querySelector('.bg-negative-tint')?.textContent.trim() === 'failed' && row.innerText.split('failed').length === 2; })()`)
	// Consecutive thoughts share one collapsed row headed by the latest.
	run("wait", "--fn", `(() => { const row = document.querySelector('[data-entry="13"]'); return !document.querySelector('[data-entry="14"]') && row?.querySelector('[data-slot=trace-collapsible-trigger]')?.textContent.includes('2 thoughts') && row.innerText.includes('Choosing a fix') && !row.innerText.includes('Checking callers'); })()`)
	run("eval", `document.querySelector('[data-entry="13"]').scrollIntoView({block:'center'})`)
	run("click", `[data-entry="13"] [data-slot=trace-collapsible-trigger]`)
	run("wait", "--fn", `document.querySelector('[data-entry="13"]')?.innerText.includes('Checking callers')`)
	run("eval", `const thoughts = document.querySelector('[data-entry="13"]').querySelectorAll('[data-slot=trace-collapsible-trigger]'); window.thoughtGroup = thoughts[0]; window.thoughtChild = thoughts[1]; window.thoughtChild.focus()`)
	run("press", "Enter")
	run("wait", "--fn", `window.thoughtChild.getAttribute('aria-expanded') === 'true'`)
	run("eval", `window.thoughtGroup.focus()`)
	run("press", "Enter")
	run("wait", "--fn", `window.thoughtGroup.getAttribute('aria-expanded') === 'false'`)
	run("press", "Enter")
	run("wait", "--fn", `window.thoughtChild.isConnected && window.thoughtChild.getAttribute('aria-expanded') === 'true' && window.thoughtGroup.getAttribute('aria-expanded') === 'true'`)
	if artifacts := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); artifacts != "" {
		run("screenshot", filepath.Join(artifacts, "thoughts-expanded.png"))
	}
	run("find", "role", "button", "click", "--name", "Output: 2 lines", "--exact")
	run("wait", "--text", "second output")
	run("find", "role", "button", "click", "--name", "Script: 2 lines", "--exact")
	run("wait", "--text", "printf second")
	run("focus", `[data-entry="6"] button[aria-expanded]`)
	run("press", "Enter")
	run("wait", "--fn", `document.querySelector('[data-entry="6"] button[aria-expanded]')?.getAttribute('aria-expanded') === 'true'`)
	thread := sessionpresentation.Thread{ID: "same-thread", Caller: "/root"}
	processes := []sessionpresentation.Process{
		{Thread: thread.ID, Item: "short", ID: "short", Command: "echo 'a  b'\n"},
		{Thread: thread.ID, Item: "multi", ID: "multi", Command: "echo first\necho second"},
		{Thread: thread.ID, Item: "wide", ID: "wide", Command: "echo " + strings.Repeat("wide ", 10)},
	}
	var attachment sessionapi.NativeRequest
	select {
	case attachment = <-attachments:
	case <-ctx.Done():
		t.Fatal("native view was not attached")
	}
	if err := machine.api.Native().Publish(attachment.Attachment, sessionapi.NativeFrame{View: sessionpresentation.View{Version: sessionpresentation.Version{Attachment: 1, Revision: 2}, Main: thread, Input: thread, Displayed: thread, Info: sessionpresentation.MainInfo{Workspace: machine.workdir}}, Processes: processes}); err != nil {
		t.Fatal(err)
	}
	for _, width := range []string{"1280", "390", "1280"} {
		run("set", "viewport", width, "900")
		wantWide := "false"
		if width == "390" {
			wantWide = "true"
		}
		run("wait", "--fn", `(() => { const rows = document.querySelectorAll('[aria-label="Active processes"] li'); return rows.length === 3 && !rows[0].querySelector('button[aria-describedby]') && getComputedStyle(rows[0].querySelector('code')).whiteSpace === 'pre' && !!rows[1].querySelector('button[aria-describedby]') && !!rows[2].querySelector('button[aria-describedby]') === `+wantWide+`; })()`)
		if width == "390" {
			run("wait", "--fn", `(() => { const code = document.querySelectorAll('[aria-label="Active processes"] li')[2].querySelector('code'); return code.scrollWidth > code.clientWidth && getComputedStyle(code).textOverflow === 'ellipsis' && !Array.from(code.children).some(child => getComputedStyle(child).display === 'inline-block'); })()`)
		}
		if artifacts := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); artifacts != "" {
			run("screenshot", filepath.Join(artifacts, "process-disclosures-"+width+".png"))
		}
	}
	run("find", "role", "button", "click", "--name", "echo first …", "--exact")
	run("wait", "--fn", `document.querySelector('[role="tooltip"]:popover-open')?.innerText.includes('echo second')`)
	run("press", "Escape")
	run("wait", "--fn", `!document.querySelector('[role="tooltip"]:popover-open')`)
}
