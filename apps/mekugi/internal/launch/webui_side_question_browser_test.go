package launch

import (
	"context"
	json "encoding/json/v2"
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
)

func TestWebUIBrowserSideQuestion(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_WEBUI_BROWSER") != "1" {
		t.Skip("set MEKUGI_TEST_WEBUI_BROWSER=1 after make webui")
	}
	t.Setenv("AGENT_BROWSER_ENGINE", "chrome")
	browser := browserFixtureCommand(t)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	page := httptest.NewServer(webui.Handler())
	defer page.Close()
	machine := newWebUIBrowserMachine(t, ctx, page.URL, "Side question", strings.Repeat("d", 64))
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.Thread = "same-thread"
	u.startSession(u.Thread, u.attached.cwd)
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": u.Thread, "turn": map[string]any{"id": "turn"}})
	u.attached.session.Lifecycle(u.Thread).Responding = true
	summaryEnabled := true
	answers := map[string]string{
		"long":        strings.Repeat("Long supporting details.\n\n", 12),
		"structured":  "## Summary\nParser review complete.\n\n[evidence]: https://example.com/evidence\n\n## Details\nSupporting parser evidence. See [evidence].",
		"sectionless": "## Summary\nSectionless parser review.\n\n" + strings.Repeat("Sectionless supporting paragraph.\n\n", 12),
	}
	observe := func(status string) {
		item := sideQuestionActivityItem(t, status)
		method := "item/completed"
		if status == "inProgress" {
			method = "item/started"
		} else if status == "failed" {
			item.ID = "failed-side-question"
		} else if answer, ok := answers[status]; ok {
			item.ID, item.Status = status+"-side-question", "completed"
			result, err := json.Marshal(map[string]any{"content": []map[string]string{{"type": "text", "text": `{"thread_id":"worker-thread","task_name":"/root/worker","answer":` + fmt.Sprintf("%q", answer) + `}`}}})
			if err != nil {
				t.Fatal(err)
			}
			item.Result = result
		}
		appServerTestNotify(t, u, method, map[string]any{"threadId": u.Thread, "turnId": "turn", "item": item})
		u.attached.childSource.Apply(sessionpresentation.ActivityEvent{Agents: u.attached.session.Roster()})
		agents := u.attached.childSource.Publication(u.attached.presentation, u.attached.source)
		if len(agents.Roster) != 1 || !agents.Roster[0].Agent.Responding {
			t.Fatalf("expected a working questioner: %+v", agents.Roster)
		}
		if err := machine.publishFrame(sessionapi.Frame{Session: sessionapi.Session{Thread: "same-thread", Title: "Side question thread", Caller: "/root", Workspace: u.attached.cwd}, Input: &sessionapi.Input{}, Main: u.attached.source.Publication(u.attached.presentation, nil), Agents: agents, ChildReplySummary: summaryEnabled}); err != nil {
			t.Fatal(err)
		}
	}
	observe("inProgress")
	identity, err := exec.CommandContext(ctx, browser, "session", "id", "--scope", "worktree", "--prefix", "webui-side-question").CombinedOutput()
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
			if directory := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); directory != "" {
				_ = exec.CommandContext(ctx, browser, "--session", name, "screenshot", filepath.Join(directory, fmt.Sprintf("side-question-failure-%d.png", time.Now().UnixNano()))).Run()
			}
			t.Fatalf("browser %s: %v %s", args[0], err, out)
		}
		return string(out)
	}
	run("open", page.URL)
	run("find", "label", "Machine name", "fill", "Side question")
	run("find", "label", "Connector URL", "fill", machine.remote.URL)
	run("find", "label", "Access token", "fill", strings.Repeat("d", 64))
	run("find", "role", "button", "click", "--name", "Connect", "--exact")
	run("wait", "--text", "Side question thread")
	run("find", "text", "Side question thread", "click", "--exact")
	run("wait", "--text", "pending")
	run("wait", "--text", "Are the parser tests ready?")
	run("wait", "--fn", `!!document.querySelector('[data-entry="1"] [data-ai-element="agent-progress"] .animate-blink svg')`)
	run("wait", "--fn", `!![...document.querySelectorAll('aside li button')].find(row => row.innerText.includes('Side question') && row.innerText.includes('pending') && !row.innerText.includes('→'))`)
	observe("completed")
	run("wait", "--fn", `!![...document.querySelectorAll('aside li button')].find(row => row.innerText.includes('Side question') && row.innerText.includes('answered') && !row.innerText.includes('→'))`)
	observe("failed")
	observe("long")
	for _, width := range []int{1280, 390} {
		run("set", "viewport", fmt.Sprint(width), "900")
		for _, theme := range []string{"light", "dark"} {
			run("set", "media", theme)
			run("wait", "--fn", `(() => { const row = document.querySelector('[data-entry="1"]'); return row?.innerText.includes('Side question') && row.innerText.includes('answered') && row.innerText.includes('Are the parser tests ready?') && row.innerText.includes('The parser tests pass.') && row.querySelector('strong')?.textContent === 'parser' && row.querySelector('[data-ai-element="agent-progress"] svg') && !row.querySelector('.animate-blink') && !row.innerText.includes('unconfirmed') && !row.innerText.includes('pending') && document.body.innerText.includes('Target unavailable') && row.getBoundingClientRect().right <= window.innerWidth && document.documentElement.scrollWidth <= window.innerWidth; })()`)
			if directory := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); directory != "" {
				run("screenshot", filepath.Join(directory, fmt.Sprintf("side-question-%s-%d.png", theme, width)))
			}
		}
	}
	run("find", "role", "button", "click", "--name", "Show full message", "--exact")
	run("wait", "--fn", `(() => { const row = [...document.querySelectorAll('[data-entry]')].find(row => row.innerText.includes('Long supporting details.')); return row?.innerText.split('Long supporting details.').length === 13 && row.querySelector('button[aria-expanded="true"]') && document.documentElement.scrollWidth <= window.innerWidth; })()`)
	if directory := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); directory != "" {
		run("screenshot", filepath.Join(directory, "side-question-expanded-dark-390.png"))
	}
	run("eval", `[...document.querySelectorAll('button')].find(button => button.textContent.trim() === 'Show less').focus()`)
	run("press", "Enter")
	observe("structured")
	observe("sectionless")
	run("eval", `(() => { const scroller = document.querySelector('[aria-label="Transcript"] .overflow-y-auto'); scroller.scrollTop = scroller.scrollHeight; })()`)
	run("eval", `window.structuredReply = () => [...document.querySelectorAll('[data-entry]')].find(row => row.textContent.includes('Parser review complete.')); window.sectionlessReply = () => [...document.querySelectorAll('[data-entry]')].find(row => row.textContent.includes('Sectionless parser review.'));`)
	for _, width := range []int{1280, 390} {
		run("set", "viewport", fmt.Sprint(width), "900")
		for _, theme := range []string{"light", "dark"} {
			run("set", "media", theme)
			run("wait", "--fn", `(() => { const row = window.structuredReply(); const short = window.sectionlessReply(); const clipped = short?.querySelector('.overflow-clip'); return row?.innerText.includes('Parser review complete.') && row.innerText.includes('Are the parser tests ready?') && !row.innerText.includes('Supporting parser evidence.') && !row.querySelector('h2') && row.querySelector('summary')?.textContent === 'Details' && short?.innerText.includes('Sectionless parser review.') && !short.querySelector('h2') && !short.querySelector('details') && short.querySelector('button[aria-expanded="false"]')?.textContent.trim() === 'Show full reply' && clipped?.getBoundingClientRect().height <= 110 && clipped.firstElementChild.getBoundingClientRect().height > 154 && document.documentElement.scrollWidth <= window.innerWidth; })()`)
			run("eval", `window.structuredReply().scrollIntoView({block:'center'})`)
			if directory := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); directory != "" {
				run("screenshot", filepath.Join(directory, fmt.Sprintf("side-question-summary-%s-%d.png", theme, width)))
			}
		}
	}
	// Native disclosure remains keyboard operable and preserves reference definitions.
	run("eval", `window.structuredReply().querySelector('summary').focus()`)
	run("press", "Enter")
	run("wait", "--fn", `(() => { const row = window.structuredReply(); return row.querySelector('details').open && row.innerText.includes('Supporting parser evidence.') && row.innerText.split('Parser review complete.').length === 2 && row.querySelector('details a[href="https://example.com/evidence"]'); })()`)
	run("eval", `window.sectionlessReply().querySelector('button[aria-expanded]').focus()`)
	run("press", "Enter")
	run("wait", "--fn", `window.sectionlessReply().innerText.split('Sectionless supporting paragraph.').length === 13`)
	if directory := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); directory != "" {
		run("screenshot", filepath.Join(directory, "side-question-summary-expanded.png"))
	}
	summaryEnabled = false
	observe("structured")
	run("wait", "--fn", `(() => { const row = window.structuredReply(); return row.querySelector('h2')?.textContent === 'Summary' && !row.querySelector('details') && window.sectionlessReply().querySelector('h2')?.textContent === 'Summary'; })()`)
}
