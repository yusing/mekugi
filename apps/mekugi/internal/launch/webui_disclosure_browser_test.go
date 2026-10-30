package launch

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/webui"
	"github.com/yusing/mekugi/contracts/observation"
	modelattachments "github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
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
		{Kind: "tool", Command: activity.Command{UserShell: true, Display: "printf complete", Tail: []string{"complete"}}},
		{Kind: "tool", Command: activity.Command{UserShell: true, Display: "printf two", Tail: []string{"first output", "second output"}}},
		{Kind: "command", Details: &activity.ItemDetails{CommandText: "printf first\nprintf second"}},
		{Kind: "reply", Agent: "/root/worker", Message: &activity.DirectedContent{From: "/root/worker", To: "/root", Text: "Complete short reply."}},
		{Kind: "reply", Agent: "/root/worker", Message: &activity.DirectedContent{From: "/root/worker", To: "/root", Text: strings.Repeat("Long reply paragraph.\n\n", 14)}},
		{Kind: "tool", Command: activity.Command{UserShell: true, Display: "printf clipped", Tail: []string{"bounded…"}, TailClipped: true}},
		{Kind: "error", Agent: "/root/worker", Text: "Complete short failure."},
		{Kind: "hook", Text: "Hook complete", Details: &activity.ItemDetails{Hook: &activity.HookDetails{Status: "completed"}}},
		{Kind: "hook", Text: "Hook output", Details: &activity.ItemDetails{Hook: &activity.HookDetails{Status: "completed"}}, Command: activity.Command{Tail: []string{"Hidden hook output"}}},
		{Kind: "hook", Text: "Hook failure", Details: &activity.ItemDetails{Hook: &activity.HookDetails{Status: "failed"}}, Command: activity.Command{Tail: []string{"Visible hook failure"}}},
		{Kind: "hook", Text: strings.Repeat("Long hook label ", 20), Details: &activity.ItemDetails{Hook: &activity.HookDetails{Status: "completed"}}},
		{Kind: "reasoning", Text: "**Checking callers**\n\nFirst thought body."},
		{Kind: "reasoning", Text: "**Choosing a fix**\n\nSecond thought body."},
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
	mcp := activity.ItemContent{Seq: 15, Agent: "Main", Kind: "tool", Text: activity.MCPText(observation.Item{Server: "plane", Tool: "workitem_relation", Status: "failed"})}
	publication.Entries = append(publication.Entries, activity.Entry{Facts: mcp.Facts, Values: mcp.Values()})
	// Skills attached without source paths keep their names and outputs.
	skillFrames := slices.Concat(composer.SkillFrames("review", "", "# Review\n\nCheck callers.\n"), composer.SkillFrames("commit", "", "# Commit\n\nUse conventional subjects.\n\nSign off.\n"))
	skillContent, err := json.Marshal([]map[string]string{{"type": "text", "text": modelattachments.Encode(skillFrames)}})
	if err != nil {
		t.Fatal(err)
	}
	skills := activity.ItemContent{Seq: 16, Agent: "Main", Kind: "attachments", Details: &activity.ItemDetails{Attachments: sessionpresentation.AttachmentValues(machine.workdir, skillContent)}}
	publication.Entries = append(publication.Entries, activity.Entry{Facts: skills.Facts, Values: skills.Values()})
	for i, command := range []string{
		`python3 -I -c 'print("ok")'`,
		"bun --input-type=module <<'JS'\nconst items = [1, 2, 3]\nfor (const item of items) {\n  const doubled = item * 2\n  console.log(doubled)\n}\nconsole.log(\"done\")\nJS\n",
		`python3 -c 'import json; rows = [{"name": name, "size": len(name)} for name in ["alpha", "beta", "gamma", "delta"]]; print(json.dumps(sorted(rows, key=lambda row: row["size"]), indent=2))'`,
		"bun run - <<'JS'\nimport { query } from './sdk.mjs';\nconsole.log(await query());\nJS\n",
		"python3 - <<'PY'\nfrom pathlib import Path\nPath('file').write_text('new')\nprint('saved')\nPY\nprintf done\n",
		"python3 - <<'PY'\nfrom pathlib import Path\nPath('running-file').write_text('new')\nPY\n",
	} {
		program := activity.ItemContent{Seq: uint64(17 + i), Agent: "Main", Kind: "tool", Text: activity.ShellSource(command, ""), Command: activity.Command{Script: command, Display: command, Exited: true}}
		if i == 4 {
			program.Command.Captured, program.Command.CaptureText, program.Command.Duration = true, "Edit `file` · +1 −1 · python3", time.Second
			program.Command.Tail = []string{"saved", "done"}
			program.Details = &activity.ItemDetails{CommandText: command}
		}
		if i == 5 {
			program.Command.Running, program.Command.Exited = true, false
			program.Details = &activity.ItemDetails{CommandText: command}
		}
		publication.Entries = append(publication.Entries, activity.Entry{Facts: program.Facts, Values: program.Values()})
	}
	const reloadCommand = `mreload '{"sources":[{"path":"private-context.md"}]}'`
	reload := activity.ItemContent{Seq: 23, Agent: "Main", Kind: "tool", Text: activity.ShellSource(reloadCommand, ""), Command: activity.Command{Script: reloadCommand, Display: reloadCommand, Exited: true, Tail: []string{"Reload snapshot: mread pearl"}}, Details: &activity.ItemDetails{CommandText: reloadCommand}}
	publication.Entries = append(publication.Entries, activity.Entry{Facts: reload.Facts, Values: reload.Values()})
	var status sessionpresentation.Label
	publish := func() {
		t.Helper()
		if err := machine.publishFrame(sessionapi.Frame{Session: sessionapi.Session{Thread: "same-thread", Title: "Disclosure thread", Workspace: machine.workdir}, Input: &sessionapi.Input{Stoppable: true}, Main: publication, Status: status}); err != nil {
			t.Fatal(err)
		}
	}
	// revise republishes one entry with a changed value list. A publication
	// is immutable once published, so each revision publishes a copy.
	revise := func(seq uint64, change func([]activity.Value) []activity.Value) {
		t.Helper()
		next := *publication
		next.Entries = slices.Clone(publication.Entries)
		publication = &next
		for i := range publication.Entries {
			if entry := &publication.Entries[i]; entry.Seq == seq {
				entry.Values = change(slices.Clone(entry.Values))
				entry.Revision++
				publish()
				return
			}
		}
		t.Fatalf("entry %d is absent", seq)
	}
	publish()
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
	for _, width := range []string{"1280", "768", "390"} {
		run("set", "viewport", width, "900")
		for _, theme := range []string{"light", "dark"} {
			run("set", "media", theme)
			run("wait", "--fn", `(() => { const welcome = document.querySelector('[aria-labelledby="welcome-heading"]'); const fields = [...welcome.querySelectorAll('input')]; return !welcome.querySelector('[data-slot="card"]') && fields.length === 3 && fields.every(field => { const box = field.getBoundingClientRect(); return box.left >= 0 && box.right <= innerWidth; }); })()`)
			if artifacts := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); artifacts != "" {
				run("screenshot", filepath.Join(artifacts, "welcome-"+width+"-"+theme+".png"))
			}
		}
	}
	run("set", "viewport", "1280", "900")
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
			// Tool rows sit on the page without a card frame, whether or not
			// output shows beneath them; output keeps its raised well.
			run("wait", "--fn", `(() => { const row = n => document.querySelector('[data-entry="'+n+'"]'); return [2,3,4,7].every(n => { const tool = row(n).querySelector('[data-ai-element=tool]'); const frame = tool?.closest('article > div > *'); return tool && !row(n).querySelector('[data-slot=card]') && getComputedStyle(frame).borderTopWidth === '0px' && getComputedStyle(frame).boxShadow === 'none'; }) && getComputedStyle(row(2).querySelector('pre')).backgroundColor !== 'rgba(0, 0, 0, 0)'; })()`)
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
	run("eval", `document.querySelector('[data-entry="16"]').scrollIntoView({block:'center'})`)
	run("wait", "--fn", `(() => { const rows = [...document.querySelectorAll('[data-entry="16"] [data-ai-element=tool]')]; return rows.length === 2 && ['review', 'commit'].every((name, i) => rows[i].innerText.includes('Attached skill') && rows[i].innerText.includes(name)) && rows[0].querySelector('button[aria-label="Output: 4 lines"]') && rows[1].querySelector('button[aria-label="Output: 6 lines"]'); })()`)
	run("click", `[data-entry="16"] button[aria-label="Output: 6 lines"]`)
	run("wait", "--fn", `(() => { const rows = document.querySelectorAll('[data-entry="16"] [data-ai-element=tool]'); return rows[1].querySelector('pre')?.textContent.includes('Use conventional subjects.') && !rows[0].querySelector('pre'); })()`)
	run("click", `[data-entry="16"] button[aria-label="Output: 4 lines"]`)
	run("wait", "--fn", `document.querySelectorAll('[data-entry="16"] [data-ai-element=tool]')[0].querySelector('pre')?.textContent.includes('Check callers.')`)
	if artifacts := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); artifacts != "" {
		run("eval", `document.querySelector('[data-entry="16"]').scrollIntoView({block:'center'})`)
		run("screenshot", filepath.Join(artifacts, "skill-attachments.png"))
	}
	// An inline program heads its row, highlighted in its own language and
	// followed by its interpreter, beside it when it fits; a longer one shows its
	// first line or an excerpt, with the full source behind its disclosure.
	for _, width := range []string{"1280", "390"} {
		run("set", "viewport", width, "900")
		for _, theme := range []string{"light", "dark"} {
			run("eval", fmt.Sprintf("document.documentElement.dataset.theme = %q", theme))
			run("eval", `document.querySelector('[data-entry="18"]').scrollIntoView({block:'center'})`)
			run("wait", "--fn", `(() => { const tool = n => document.querySelector('[data-entry="'+n+'"] [data-ai-element=tool]'); return [[17, 'print("ok")', '· via python3 -I'], [18, 'const items = [1, 2, 3] …', '· via bun --input-type=module']].every(([n, head, via]) => { const row = tool(n); const code = row?.querySelector('div > code'); const label = code?.nextElementSibling; if (!code || !label || code.textContent !== head || label.innerText.trim() !== via || row.querySelector('pre')) return false; const a = code.getBoundingClientRect(), b = label.getBoundingClientRect(), r = row.getBoundingClientRect(); const beside = b.top < a.bottom && b.bottom > a.top && b.left >= a.right; return code.querySelectorAll('span[style]').length > 1 && (beside || n === 18 && b.top >= a.bottom - 2) && b.right <= r.right + 0.5; }) && tool(18).querySelector('button[aria-label="Script: 6 lines"]') && !tool(17).querySelector('button[aria-label^="Script"]'); })()`)
			run("wait", "--fn", `(() => { const row = document.querySelector('[data-entry="19"] [data-ai-element=tool]'); const code = row?.querySelector('div > code'); const label = code?.nextElementSibling; const r = row?.getBoundingClientRect(); return !!code && code.textContent === 'import json; rows = [{"name": name, "size": len(name)} for name in ["alpha", "be …' && label?.innerText.trim() === '· via python3' && !row.querySelector('pre') && !!row.querySelector('button[aria-label="Script: 1 line"]') && code.getBoundingClientRect().right <= r.right + 0.5 && label.getBoundingClientRect().right <= r.right + 0.5; })()`)
			run("wait", "--fn", `(() => { const row = document.querySelector('[data-entry="21"] [data-ai-element=tool]'); return row?.innerText.includes('from pathlib import Path') && row.innerText.includes('via python3') && row.querySelector('button[aria-label="Script: 3 lines"]') && !document.body.innerText.includes('private-context.md') && !document.body.innerText.includes('Reload snapshot: mread pearl'); })()`)
			run("wait", "--fn", `(() => { const row = document.querySelector('[data-entry="22"]'); return row?.innerText.includes('from pathlib import Path') && row.innerText.includes('via python3') && row.querySelector('button[aria-label="Script: 2 lines"]') && row.innerText.includes('running'); })()`)
			if artifacts := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); artifacts != "" {
				run("screenshot", filepath.Join(artifacts, "interpreter-programs-"+width+"-"+theme+".png"))
			}
		}
		run("click", `[data-entry="19"] button[aria-label="Script: 1 line"]`)
		run("wait", "--fn", `(() => { const pre = document.querySelector('[data-entry="19"] pre'); return pre?.textContent.includes('key=lambda row: row["size"]), indent=2))') && pre.querySelectorAll('span[style]').length > 5 && pre.getBoundingClientRect().right <= document.querySelector('[data-entry="19"]').getBoundingClientRect().right + 0.5; })()`)
		if artifacts := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); artifacts != "" {
			run("screenshot", filepath.Join(artifacts, "interpreter-program-long-open-"+width+".png"))
		}
		run("click", `[data-entry="19"] button[aria-label="Script: 1 line"]`)
		run("wait", "--fn", `!document.querySelector('[data-entry="19"] pre')`)
		run("click", `[data-entry="21"] button[aria-label="Script: 3 lines"]`)
		run("wait", "--fn", `(() => { const pre = document.querySelector('[data-entry="21"] pre'); return pre?.textContent.includes("Path('file').write_text('new')") && !pre.textContent.includes('python3 -') && pre.querySelectorAll('span[style]').length > 5; })()`)
		if artifacts := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); artifacts != "" {
			run("eval", `document.querySelector('[data-entry="21"]').scrollIntoView({block:'center'})`)
			run("screenshot", filepath.Join(artifacts, "captured-interpreter-open-"+width+".png"))
		}
		run("click", `[data-entry="21"] button[aria-label="Script: 3 lines"]`)
	}
	run("set", "viewport", "1280", "900")
	run("click", `[data-entry="18"] button[aria-label="Script: 6 lines"]`)
	run("wait", "--fn", `(() => { const pre = document.querySelector('[data-entry="18"] pre'); return pre?.textContent.includes('console.log("done")') && pre.querySelectorAll('span[style]').length > 5; })()`)
	if artifacts := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); artifacts != "" {
		run("screenshot", filepath.Join(artifacts, "interpreter-program-open.png"))
	}
	run("find", "role", "button", "click", "--name", "Output: 2 lines", "--exact")
	run("wait", "--text", "second output")
	run("wait", "--fn", `(() => { const row = document.querySelector('[data-entry="3"]'); return row.querySelector('pre')?.textContent.includes('second output'); })()`)
	run("find", "role", "button", "click", "--name", "Script: 2 lines", "--exact")
	run("wait", "--text", "printf second")
	run("focus", `[data-entry="6"] button[aria-expanded]`)
	run("press", "Enter")
	run("wait", "--fn", `document.querySelector('[data-entry="6"] button[aria-expanded]')?.getAttribute('aria-expanded') === 'true'`)
	// A row that gains content keeps its tree: open disclosures, their
	// controls and focus survive the change.
	run("eval", `window.scriptToggle = document.querySelector('[data-entry="4"] button[aria-label="Script: 2 lines"]')`)
	revise(4, func(values []activity.Value) []activity.Value { values[0].Running = true; return values })
	run("wait", "--fn", `document.querySelector('[data-entry="4"]').innerText.includes('running') && window.scriptToggle.isConnected && window.scriptToggle.getAttribute('aria-expanded') === 'true'`)
	revise(4, func(values []activity.Value) []activity.Value {
		values[0].Tail = []string{"first output line"}
		return values
	})
	run("wait", "--fn", `(() => { const row = document.querySelector('[data-entry="4"]'); return row.innerText.includes('first output line') && row.innerText.includes('printf second') && window.scriptToggle.isConnected && window.scriptToggle.getAttribute('aria-expanded') === 'true'; })()`)
	run("eval", `window.outputToggle = document.querySelector('[data-entry="3"] button[aria-label="Output: 2 lines"]'); window.outputToggle.focus()`)
	run("wait", "--fn", `document.activeElement === window.outputToggle && window.outputToggle.getAttribute('aria-expanded') === 'true'`)
	revise(3, func(values []activity.Value) []activity.Value {
		return append(values, activity.Value{Kind: "op", Verb: "Edit", Path: "edited.txt"})
	})
	run("wait", "--fn", `(() => { const row = document.querySelector('[data-entry="3"]'); return row.innerText.includes('edited.txt') && row.querySelector('pre')?.textContent.includes('second output') && window.outputToggle.isConnected && document.activeElement === window.outputToggle && window.outputToggle.getAttribute('aria-expanded') === 'true'; })()`)
	// A long status truncates in its badge without covering the title bar's
	// other controls or collapsing the title.
	next := *publication
	publication = &next
	status = sessionpresentation.Label{Text: "Waiting for worker to finish reviewing the implementation and running the remaining validation checks", Running: true}
	publish()
	for _, width := range []string{"768", "1280"} {
		run("set", "viewport", width, "900")
		run("wait", "--fn", `(() => { const header = document.querySelector('header'); const badge = [...header.querySelectorAll('[data-slot=badge]')].find(b => b.title.startsWith('Waiting for worker')); if (!badge) return false; const text = badge.querySelector('.truncate'); const parts = [...new Set([header.querySelector('h1'), header.querySelector('#thread-workspace'), ...header.querySelectorAll('button, [data-slot=badge]'), ...header.querySelectorAll('.contents > span')])].filter(e => e && e.getBoundingClientRect().width > 0).map(e => e.getBoundingClientRect()).sort((a, b) => a.left - b.left); const h = header.getBoundingClientRect(); return text.scrollWidth > text.clientWidth && header.querySelector('h1').getBoundingClientRect().width >= 16 && parts.every((r, i) => r.left >= h.left && r.right <= h.right + 0.5 && (i === 0 || parts[i - 1].right <= r.left + 0.5)); })()`)
		if artifacts := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); artifacts != "" {
			run("screenshot", filepath.Join(artifacts, "long-status-"+width+".png"))
		}
	}
	status = sessionpresentation.Label{}
	next = *publication
	publication = &next
	publish()
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
