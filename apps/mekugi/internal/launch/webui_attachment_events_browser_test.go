package launch

import (
	"context"
	json "encoding/json/v2"
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
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

func TestWebUIBrowserAttachmentEvents(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_WEBUI_BROWSER") != "1" {
		t.Skip("set MEKUGI_TEST_WEBUI_BROWSER=1 after make webui")
	}
	t.Setenv("AGENT_BROWSER_ENGINE", "chrome")
	browser := browserFixtureCommand(t)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	page := httptest.NewServer(webui.Handler())
	defer page.Close()
	machine := newWebUIBrowserMachine(t, ctx, page.URL, "Attachments", strings.Repeat("f", 64))
	frames := composer.FileFrames("/workspace/source.go", strings.Repeat("retained source ", 50)+"\nlast line")
	frames = append(frames, composer.SkillFrames("review", "", "# Review\nCheck callers.")...)
	frames = append(frames, composer.SkillFrames("commit", "", "# Commit\nPreserve intent.")...)
	frames = append(frames, `Attached file "/workspace/missing.go": CONTENT NOT ATTACHED ("not found"). Read separately.`, attachments.FormatSelection("[Selected text]", "screen", "quoted source"))
	content, err := json.Marshal([]map[string]string{{"type": "text", "text": (attachments.Snapshot{ID: "submitted", Frames: frames}).Encode()}})
	if err != nil {
		t.Fatal(err)
	}
	item := activity.ItemContent{Seq: 1, Agent: "Main", Kind: "attachments", Details: &activity.ItemDetails{Attachments: sessionpresentation.AttachmentValues("/workspace", content)}}
	event := (activity.Record[activity.Entry]{Source: activity.Entry{Facts: item.Facts}, Values: item.Values()}).Publish()
	publication := &activity.Publication{Entries: []activity.Entry{event}}
	roster := &activity.Publication{Roster: []activity.AgentRow{{Agent: activity.Agent{AgentFacts: activity.AgentFacts{Name: "/root/worker", Thread: "child", Responding: true}, Status: "responding", Summary: []activity.Entry{event}}}}}
	if err := machine.publishFrame(sessionapi.Frame{Session: sessionapi.Session{Thread: "same-thread", Title: "Attachment events", Workspace: machine.workdir}, Main: publication, Agents: roster}); err != nil {
		t.Fatal(err)
	}
	identity, err := exec.CommandContext(ctx, browser, "session", "id", "--scope", "worktree", "--prefix", "webui-attachments").CombinedOutput()
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
		output, err := exec.CommandContext(ctx, browser, append([]string{"--session", name}, args...)...).CombinedOutput()
		if err != nil {
			state, _ := exec.CommandContext(ctx, browser, "--session", name, "eval", `document.body.innerText.slice(0,4000)`).CombinedOutput()
			t.Fatalf("browser %s: %v: %s\n%s", args[0], err, output, state)
		}
		return string(output)
	}
	run("open", page.URL)
	run("find", "label", "Machine name", "fill", "Attachments")
	run("find", "label", "Connector URL", "fill", machine.remote.URL)
	run("find", "label", "Access token", "fill", strings.Repeat("f", 64))
	run("find", "role", "button", "click", "--name", "Connect", "--exact")
	run("find", "text", "Attachments thread", "click", "--exact")
	run("wait", "--fn", `(() => { const row = document.querySelector('[data-entry="1"]'); return row && row.querySelectorAll('[data-ai-element=tool]').length === 5 && row.innerText.split('Attach').length === 2 && row.innerText.includes('missing.go') && row.innerText.includes('not found'); })()`)
	run("wait", "--text", "Attach 2 files, 2 skills, 1 selection")
	for _, width := range []string{"1280", "390"} {
		run("set", "viewport", width, "900")
		for _, theme := range []string{"light", "dark"} {
			run("eval", `document.documentElement.dataset.theme = '`+theme+`'`)
			run("wait", "--fn", `[...document.querySelectorAll('[data-entry="1"] [data-ai-element=tool]')].every(row => { const box = row.getBoundingClientRect(); return box.left >= 0 && box.right <= innerWidth; })`)
			if artifacts := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); artifacts != "" {
				run("screenshot", filepath.Join(artifacts, "attachment-events-"+width+"-"+theme+".png"))
			}
		}
	}
	run("set", "viewport", "1280", "900")
	run("eval", `document.querySelector('[data-entry="1"] button[aria-label="Output: 2 lines"]').click()`)
	run("wait", "--fn", `document.querySelector('[data-entry="1"] pre')?.textContent.includes('last line')`)
	if artifacts := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); artifacts != "" {
		run("screenshot", filepath.Join(artifacts, "attachment-events-content.png"))
	}
}
