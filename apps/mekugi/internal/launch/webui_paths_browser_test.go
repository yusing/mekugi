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
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/webui"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
)

func TestWebUIBrowserPathSuffixes(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_WEBUI_BROWSER") != "1" {
		t.Skip("set MEKUGI_TEST_WEBUI_BROWSER=1 after make webui")
	}
	t.Setenv("AGENT_BROWSER_ENGINE", "chrome")
	browser := browserFixtureCommand(t)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	page := httptest.NewServer(webui.Handler())
	defer page.Close()
	token := strings.Repeat("f", 64)
	const workspace = "/home/dev/projects/workspace/.worktrees/distinctive-project"
	const path = workspace + "/a directory/with many levels/界面/very_long_distinctive_filename.go"
	var machine *webUIBrowserMachine
	machine = newWebUIBrowserMachineWithNative(t, ctx, page.URL, "Paths", token, func(native *sessionapi.Native, request sessionapi.NativeRequest) bool {
		if !request.Attach {
			return false
		}
		thread := sessionpresentation.Thread{ID: "same-thread", Caller: "/root"}
		view := sessionpresentation.View{Version: sessionpresentation.Version{Attachment: 1, Revision: 1}, Main: thread, Input: thread, Displayed: thread, Info: sessionpresentation.MainInfo{Workspace: workspace}, Interrupt: sessioncontrol.InterruptTarget{Thread: "same-thread", Turn: "fixture-turn"}}
		family, _ := machine.hub.Family("same-thread")
		if err := native.Publish(request.Attachment, sessionapi.NativeFrame{Family: family, View: view}); err != nil {
			request.Reply <- err.Error()
		} else {
			request.Reply <- ""
		}
		return true
	})
	values := []activity.Value{
		{Kind: "op", Verb: "Read", Path: path},
		{Kind: "op", Verb: "View", Path: path, Workdir: workspace, ShowWorkdir: true},
		{Kind: "op", Verb: "Read", Changes: []activity.ChangeRow{{Code: "M", Label: path, Added: 2, Removed: 1}}},
	}
	main := &activity.Publication{}
	for i, value := range values {
		main.Entries = append(main.Entries, activity.Entry{Seq: uint64(i + 1), Agent: "Main", Kind: "tool", Values: []activity.Value{value}, Revision: 1})
	}
	frame := sessionapi.Frame{Session: sessionapi.Session{Thread: "same-thread", Caller: "/root", Title: "Paths thread", Workspace: workspace}, Input: &sessionapi.Input{}, Main: main}
	if err := machine.publishFrame(frame); err != nil {
		t.Fatal(err)
	}
	identity, err := exec.CommandContext(ctx, browser, "session", "id", "--scope", "worktree", "--prefix", "webui-paths").CombinedOutput()
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
			state, _ := exec.CommandContext(ctx, browser, "--session", name, "eval", `document.body.innerText.slice(0, 2000)`).CombinedOutput()
			t.Fatalf("browser %s: %v %s\n%s", args[0], err, out, state)
		}
		return string(out)
	}
	run("open", page.URL)
	run("set", "viewport", "1280", "900")
	run("find", "label", "Machine name", "fill", "Paths")
	run("find", "label", "Connector URL", "fill", machine.remote.URL)
	run("find", "label", "Access token", "fill", token)
	run("find", "role", "button", "click", "--name", "Connect", "--exact")
	run("wait", "--text", "Paths thread")
	run("find", "text", "Paths thread", "click", "--exact")
	run("wait", "--text", "very_long_distinctive_filename.go")
	if err := machine.publishFrame(frame); err != nil {
		t.Fatal(err)
	}
	for _, width := range []int{1280, 768, 390} {
		run("wait", "--fn", `document.querySelector('#thread-workspace bdi')?.textContent.endsWith('distinctive-project')`)
		run("set", "viewport", fmt.Sprint(width), "900")
		for _, theme := range []string{"light", "dark"} {
			run("set", "media", theme)
			// Check actual glyph geometry, not just CSS declarations: the last
			// character stays visible when the full source overflows to the left.
			run("wait", "--fn", `(() => {
 const paths = [...document.querySelectorAll('span[dir="rtl"]')].filter(el => el.clientWidth && el.querySelector('bdi'));
 if (paths.length < 4) return false;
 let clipped = 0;
 for (const el of paths) {
   const bdi = el.querySelector('bdi'), text = bdi.firstChild;
   if (!text?.length || !el.title.endsWith(bdi.textContent)) return false;
   const box = el.getBoundingClientRect(), range = document.createRange();
   range.setStart(text, text.length - 1); range.setEnd(text, text.length);
   const last = range.getBoundingClientRect(), css = getComputedStyle(el);
   if (box.right > innerWidth + 1 || last.left < box.left - 1 || last.right > box.right + 1 || css.direction !== 'rtl' || css.textOverflow !== 'ellipsis' || getComputedStyle(bdi).direction !== 'ltr') return false;
   range.selectNodeContents(bdi); const selection = getSelection(); selection.removeAllRanges(); selection.addRange(range);
   if (selection.toString() !== bdi.textContent) return false;
   selection.removeAllRanges();
   if (el.scrollWidth > el.clientWidth) clipped++;
 }
 return clipped > 0 && document.documentElement.scrollWidth <= innerWidth;
})()`)
			if directory := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); directory != "" {
				run("screenshot", filepath.Join(directory, fmt.Sprintf("path-suffixes-%s-%d.png", theme, width)))
			}
		}
	}
}
