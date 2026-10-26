package launch

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/connector"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/webui"
	"github.com/yusing/mekugi/libraries/diffmodel"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	changes "github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/modules/conversation/completion"
	"github.com/yusing/mekugi/modules/conversation/composer"
	"github.com/yusing/mekugi/modules/conversation/prompt"
	"github.com/yusing/mekugi/modules/livepreview/preview"
	threadcontrol "github.com/yusing/mekugi/modules/threadcontrol/control"
)

// This opt-in acceptance drives the embedded application through its visible
// controls. Fixture owners acknowledge interrupts without launching a backend.
func TestWebUIBrowserMachines(t *testing.T) {
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
	response, err := http.Get(page.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatal("built application is unavailable; run make webui before this test")
	}
	type pendingAttachment struct {
		native  *sessionapi.Native
		request sessionapi.NativeRequest
	}
	var delayedChild atomic.Bool
	childAttachment := make(chan pendingAttachment, 1)
	childFile := make(chan sessionapi.NativeRequest, 1)
	processStop := make(chan pendingAttachment, 1)
	inputReply := make(chan chan string, 1)
	settingsCommands := make(chan sessionapi.NativeRequest, 8)
	settingsDismissal := make(chan sessionapi.NativeRequest, 1)
	var holdSettingsDismissal atomic.Bool
	browserCommands := make(chan sessionapi.NativeRequest, 4)
	references := make(chan sessionapi.NativeRequest, 1)
	var alpha *webUIBrowserMachine
	alpha = newWebUIBrowserMachineWithNative(t, ctx, page.URL, "Alpha", strings.Repeat("a", 64), func(native *sessionapi.Native, request sessionapi.NativeRequest) bool {
		command := request.Operation.Command
		menu := request.Operation.Menu
		if request.Operation.Completion != nil || request.Operation.Reference != nil || command != nil && strings.HasPrefix(*command, "/title ") {
			thread := sessionpresentation.Thread{ID: request.Thread, Caller: "/root"}
			frame := sessionapi.NativeFrame{View: sessionpresentation.View{Version: request.Version, Main: thread, Input: thread, Displayed: thread, Info: sessionpresentation.MainInfo{Workspace: request.Workspace}}}
			frame.View.Version.Revision++
			frame.Family, _ = alpha.hub.Family(request.Thread)
			if input := request.Operation.Completion; input != nil {
				frame.CompletionTarget = input.Target
				if strings.HasPrefix(input.Draft.Text, "/") {
					frame.View.Menu = &sessionpresentation.MenuView{ID: 99, View: completion.View{Choices: nativeCommands}}
				}
			} else if reference := request.Operation.Reference; reference != nil {
				references <- request
				frame.Effects = []sessionapi.NativeEffect{{ID: 2, Thread: reference.Thread, Target: &reference.Target, Selection: &sessionapi.NativeSelection{Description: reference.Description, Text: reference.Text, Source: reference.Source}}}
			} else {
				browserCommands <- request
			}
			if err := native.Publish(request.Attachment, frame); err != nil {
				request.Reply <- err.Error()
			} else {
				request.Reply <- ""
			}
			return true
		}
		if command != nil && (*command == "/model" || *command == "/effort" || strings.HasPrefix(*command, "/tier ")) || menu != nil && menu.Kind == sessionpresentation.MenuDismiss {
			thread := sessionpresentation.Thread{ID: request.Thread, Caller: "/root"}
			view := sessionpresentation.View{Version: request.Version, Main: thread, Input: thread, Displayed: thread, Info: sessionpresentation.MainInfo{Workspace: request.Workspace}}
			view.Version.Revision++
			if command != nil {
				settingsCommands <- request
				if *command == "/model" || *command == "/effort" {
					view.Menu = &sessionpresentation.MenuView{ID: view.Version.Revision, Kind: sessionpresentation.MenuSettings, Setting: *command}
				}
			}
			family, _ := alpha.hub.Family(request.Thread)
			if err := native.Publish(request.Attachment, sessionapi.NativeFrame{View: view, Family: family}); err != nil {
				request.Reply <- err.Error()
			} else if menu != nil && holdSettingsDismissal.Swap(false) {
				settingsDismissal <- request
			} else {
				request.Reply <- ""
			}
			return true
		}
		if request.Attach && request.Thread == "child" && delayedChild.CompareAndSwap(false, true) {
			childAttachment <- pendingAttachment{native, request}
			return true
		}
		if request.Operation.StopProcess != nil {
			processStop <- pendingAttachment{native, request}
			return true
		}
		if request.Operation.Details != nil {
			childFile <- request
			request.Reply <- "Child file target reached its runtime."
			return true
		}
		if request.Operation.Composer != nil && request.Operation.Composer.Intent.Operation == sessionpresentation.SubmitComposer {
			inputReply <- request.Reply
			return true
		}
		return false
	})
	alpha.childThreads = []string{"child"}
	alpha.start("child")
	beta := newWebUIBrowserMachine(t, ctx, page.URL, "Beta", strings.Repeat("b", 64))
	identity, err := exec.CommandContext(ctx, browser, "session", "id", "--scope", "worktree", "--prefix", "webui-transport").CombinedOutput()
	if err != nil {
		t.Fatalf("browser session identity: %v: %s", err, identity)
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
			snapshot, _ := exec.CommandContext(ctx, browser, "--session", name, "snapshot", "-i").CombinedOutput()
			text, _ := exec.CommandContext(ctx, browser, "--session", name, "get", "text", "[aria-label=Transcript]").CombinedOutput()
			errors, _ := exec.CommandContext(ctx, browser, "--session", name, "errors", "--json").CombinedOutput()
			t.Fatalf("browser %s failed: %v: %s\n%s\nTranscript: %s\n%s", strings.Join(args[:min(2, len(args))], " "), err, output, snapshot, text, errors)
		}
		return string(output)
	}
	button := func(label string) { t.Helper(); run("find", "role", "button", "click", "--name", label, "--exact") }
	closeDialog := func() {
		t.Helper()
		button("Close dialog")
		run("wait", "--fn", "!document.querySelector('dialog[open]')")
	}
	// Threads from every machine are links in the sidebar, under their machine.
	open := func(label string) {
		t.Helper()
		run("wait", "--text", label+" thread")
		run("find", "text", label+" thread", "click", "--exact")
		run("wait", "--fn", "document.querySelector('button[aria-label=Stop]')?.disabled === false")
	}
	// The first machine connects from the welcome form; later ones from Machines.
	first := true
	add := func(machine *webUIBrowserMachine, label, token string) {
		t.Helper()
		if !first {
			button("Machines")
			button("Add machine")
		}
		first = false
		run("find", "label", "Machine name", "fill", label)
		run("find", "label", "Connector URL", "fill", machine.remote.URL)
		run("find", "label", "Access token", "fill", token)
		button("Connect")
		run("wait", "--fn", "!document.querySelector('dialog[open]')")
		open(label)
	}
	interrupt := func(machine, other *webUIBrowserMachine) {
		t.Helper()
		button("Stop")
		select {
		case intent := <-machine.intents:
			if intent.Action != sessionapi.Stop || intent.Thread != "same-thread" {
				t.Fatalf("wrong target or action: %s %s", intent.Thread, intent.Action)
			}
		case <-ctx.Done():
			t.Fatal("selected machine did not receive its interrupt")
		}
		select {
		case <-other.intents:
			t.Fatal("request reached the other machine")
		default:
		}
	}
	reload := func(address string) {
		t.Helper()
		run("reload")
		run("wait", "--fn", "document.querySelector('button[aria-label=Stop]')?.disabled === false")
		if run("get", "url") != address {
			t.Fatal("reload did not retain the selected machine and thread address")
		}
	}

	run("--engine", "chrome", "open", page.URL)
	run("snapshot", "-i")
	add(alpha, "Alpha", strings.Repeat("a", 64))
	run("fill", "#composer-text", "/")
	run("wait", "--fn", `document.querySelector('[aria-label="Suggestions"]')`)
	choices := run("get", "text", "[aria-label=Suggestions]")
	for _, command := range []string{"/model", "/effort", "/reasoning", "/tier", "/title", "/copy", "/lock", "/unlock"} {
		if strings.Contains(choices, command) {
			t.Fatalf("redundant command visible: %s", command)
		}
	}
	if !strings.Contains(choices, "/status") {
		t.Fatal("ordinary command missing")
	}
	run("fill", "#composer-text", "keep draft")
	button("Rename thread")
	run("fill", "#thread-title", "A useful title")
	button("Save")
	run("wait", "--fn", "!document.querySelector('dialog[open]')")
	if request := <-browserCommands; *request.Operation.Command != "/title A useful title" || request.Operation.Editor != nil {
		t.Fatal("rename changed the composer")
	}
	const responseMarkdown = "Response with **authored Markdown**.\n\n```go\nfmt.Println(\"complete\")\n```"
	if err := alpha.publishFrame(sessionapi.Frame{Session: sessionapi.Session{Thread: "same-thread", Caller: "/root", Title: "Alpha thread", Workspace: alpha.workdir}, Input: &sessionapi.Input{Stoppable: true}, Main: &activitypresentation.Publication{Entries: []activitypresentation.Entry{{Facts: activitypresentation.Facts{Seq: 99, Agent: "Main", Kind: "final", Text: responseMarkdown}}}}}); err != nil {
		t.Fatal(err)
	}
	run("wait", "--text", "authored Markdown")
	run("eval", `Object.defineProperty(navigator, 'clipboard', {configurable:true, value:{writeText: async text => { window.copiedResponse = text; }}})`)
	run("wait", "--fn", `!!document.querySelector('[data-entry] button[aria-label="Copy response"]') && !document.querySelector('header button[aria-label="Copy last response"]')`)
	button("Copy response")
	run("wait", "--fn", fmt.Sprintf(`window.copiedResponse === %q`, responseMarkdown))
	select {
	case <-browserCommands:
		t.Fatal("message copy dispatched a semantic command")
	default:
	}
	if artifacts := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); artifacts != "" {
		run("screenshot", filepath.Join(artifacts, "copy-response-desktop.png"))
	}
	run("wait", "--fn", `!!document.querySelector('button[aria-label="Copy response"]')`)
	run("eval", `navigator.clipboard.writeText = async () => { throw new Error('Clipboard denied'); }`)
	button("Copy response")
	run("wait", "--text", "Could not copy the response")
	run("eval", `navigator.clipboard.writeText = async text => { window.copiedResponse = text; }`)
	if run("get", "value", "#composer-text") != "keep draft\n" {
		t.Fatal("title or message control changed draft")
	}
	// Captured temporary trees fold at the same boundary as the terminal;
	// opening them reveals retained paths without another host request.
	var captures []string
	for i := range 200 {
		captures = append(captures, fmt.Sprintf("Create `/tmp/tree/file%03d.go` +3 -0 · cp", i))
	}
	item := activitypresentation.ItemContent{Facts: activitypresentation.Facts{Seq: 1, Agent: "Main", Kind: "tool", Text: strings.Join(captures, "\n\n")}}
	frame := sessionapi.Frame{FamilyRoot: "same-thread", Members: []threadcontrol.Participant{{Thread: "same-thread", Task: "/root"}, {Thread: "child", Parent: "same-thread", Task: "/root/worker", Role: "worker"}}, Session: sessionapi.Session{Thread: "same-thread", Caller: "/root", Title: "Alpha thread", Workspace: alpha.workdir}, Input: &sessionapi.Input{Stoppable: true},
		Main: &activitypresentation.Publication{Entries: []activitypresentation.Entry{{Facts: item.Facts, Values: item.Values(), Revision: 1}}}}
	if err := alpha.publishFrame(frame); err != nil {
		t.Fatal(err)
	}
	run("wait", "--text", "190 more files")
	run("eval", `(() => { const row = document.querySelector('[aria-label="Transcript"] [data-entry]'); const range = document.createRange(); range.selectNodeContents(row); const selection = getSelection(); selection.removeAllRanges(); selection.addRange(range); row.dispatchEvent(new PointerEvent('pointerup', {bubbles:true, clientX:400, clientY:300})); })()`)
	run("wait", "--fn", `document.querySelector('button[aria-label="Copy selection to reference"]')`)
	run("wait", "--fn", `(() => { const r = document.querySelector('button[aria-label="Copy selection to reference"]').getBoundingClientRect(); return r.bottom <= 300 && r.bottom >= 270 && r.left <= 400 && r.right >= 400; })()`)
	run("set", "viewport", "390", "844")
	run("wait", "--fn", `(() => { const r = document.querySelector('button[aria-label="Copy selection to reference"]').getBoundingClientRect(); return r.left >= 7.5 && r.right <= innerWidth - 7.5 && r.top >= 7.5 && r.bottom <= innerHeight - 7.5; })()`)
	run("set", "viewport", "1280", "900")
	if artifacts := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); artifacts != "" {
		run("set", "viewport", "1280", "900")
		run("screenshot", filepath.Join(artifacts, "reference-desktop.png"))
		run("set", "viewport", "390", "844")
		run("screenshot", filepath.Join(artifacts, "reference-mobile.png"))
		run("set", "viewport", "1280", "900")
	}
	run("focus", `button[aria-label="Copy selection to reference"]`)
	run("press", "Enter")
	run("wait", "--fn", `document.querySelector('button[aria-label="Remove Selected output"]')`)
	if request := <-references; request.Operation.Reference.Thread != "same-thread" || !strings.Contains(request.Operation.Reference.Text, "file000.go") {
		t.Fatal("reference lost source or destination")
	}
	if run("get", "value", "#composer-text") != "keep draft\n" {
		t.Fatal("reference replaced the draft")
	}
	run("wait", "--fn", `document.activeElement?.id === "composer-text"`)
	button("Remove Selected output")
	// Removing the selected publication must dismiss its contextual action,
	// even though both the browser selection and cloned range collapse together.
	run("eval", `(() => { const range = document.createRange(); range.selectNodeContents(document.querySelector('[aria-label="Transcript"] [data-entry]')); getSelection().removeAllRanges(); getSelection().addRange(range); })()`)
	run("wait", "--fn", `!!document.querySelector('button[aria-label="Copy selection to reference"]')`)
	removed := frame
	removed.Main = &activitypresentation.Publication{}
	if err := alpha.publishFrame(removed); err != nil {
		t.Fatal(err)
	}
	run("wait", "--fn", `!document.querySelector('[aria-label="Transcript"] [data-entry]') && !document.querySelector('button[aria-label="Copy selection to reference"]')`)
	if err := alpha.publishFrame(frame); err != nil {
		t.Fatal(err)
	}
	run("wait", "--text", "190 more files")
	run("fill", "#composer-text", "")
	if run("eval", "document.body.innerText.includes('file199.go')") != "false\n" {
		t.Fatal("large temporary capture started expanded")
	}
	run("eval", "Array.from(document.querySelectorAll('summary')).find(row => row.textContent.includes('190 more files')).focus()")
	run("press", "Enter")
	run("wait", "--fn", "document.body.innerText.includes('file199.go')")
	frame.Main.Entries[0].Revision++
	if err := alpha.publishFrame(frame); err != nil {
		t.Fatal(err)
	}
	run("wait", "--fn", "document.body.innerText.includes('file199.go')")
	run("find", "text", "190 more files", "click", "--exact")
	run("wait", "--fn", "!document.body.innerText.includes('file199.go')")
	if artifacts := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); artifacts != "" {
		run("set", "viewport", "1280", "900")
		run("screenshot", filepath.Join(artifacts, "temporary-edits-desktop.png"))
		run("set", "viewport", "390", "844")
		run("screenshot", filepath.Join(artifacts, "temporary-edits-mobile.png"))
		run("set", "viewport", "1280", "900")
	}
	// Wait publications retain targets, timeouts and the actual settlement reason.
	frame.Members[0].Status = threadcontrol.Waiting
	frame.Agents = &activitypresentation.Publication{Roster: []activitypresentation.AgentRow{
		{Agent: activitypresentation.Agent{AgentFacts: activitypresentation.AgentFacts{Name: "/root", Thread: "same-thread", Waiting: true, Wait: &activitypresentation.Value{Kind: "progress", Body: "Waiting for", WaitTargets: []activitypresentation.WaitTarget{{Name: "/root/worker"}}, WaitTimeout: "5s"}}}},
		{Agent: activitypresentation.Agent{AgentFacts: activitypresentation.AgentFacts{Name: "/root/worker", Thread: "child"}}},
		{Agent: activitypresentation.Agent{AgentFacts: activitypresentation.AgentFacts{Name: "/root/history", Thread: "old-child"}}},
	}}
	frame.Main = &activitypresentation.Publication{Entries: []activitypresentation.Entry{
		{Facts: activitypresentation.Facts{Seq: 2, Agent: "Main", Kind: "tool"}, Values: []activitypresentation.Value{{Kind: "progress", Body: "Waiting for", WaitTargets: []activitypresentation.WaitTarget{{Name: "/root/worker"}}, WaitTimeout: "5s"}}, Revision: 1},
		{Facts: activitypresentation.Facts{Seq: 3, Agent: "Main", Kind: "tool"}, Values: []activitypresentation.Value{{Kind: "progress", Body: "Finished waiting for", WaitTargets: []activitypresentation.WaitTarget{{Name: "/root/worker"}}, WaitReason: "paused"}}, Revision: 1},
	}}
	for i := range frame.Main.Entries {
		entry := &frame.Main.Entries[i]
		entry.Text = entry.Values[0].ProgressText()
	}
	if err := alpha.publishFrame(frame); err != nil {
		t.Fatal(err)
	}
	run("wait", "--text", "Waiting for worker (timeout 5s)")
	run("wait", "--text", "Finished waiting for worker: paused")
	run("wait", "--fn", `document.querySelector('[aria-label="Thread panel"] [title="Waiting for worker (timeout 5s)"]')`)
	run("wait", "--text", "1 waiting")
	settled := frame.Main.Entries[1]
	settled.Values = []activitypresentation.Value{settled.Values[0]}
	settled.Values[0].WaitReason = "message received"
	settled.Text = settled.Values[0].ProgressText()
	settled.Revision++
	frame.Main = &activitypresentation.Publication{Entries: []activitypresentation.Entry{frame.Main.Entries[0], settled}}
	frame.Agents = &activitypresentation.Publication{Roster: append([]activitypresentation.AgentRow(nil), frame.Agents.Roster...)}
	frame.Agents.Roster[0].Agent.Waiting, frame.Agents.Roster[0].Agent.Wait = false, nil
	frame.Members[0].Status = ""
	if err := alpha.publishFrame(frame); err != nil {
		t.Fatal(err)
	}
	run("wait", "--text", "Finished waiting for worker: message received")
	run("wait", "--fn", `(() => { const row = document.querySelector('[aria-label="Thread panel"] button[aria-current=true]'); return row?.textContent.includes('Idle') && !row.textContent.includes('Waiting'); })()`)
	// Independent runtimes publish their own summary through the roster, without
	// copying the child's transcript into the parent's feed.
	frame.Agents = &activitypresentation.Publication{Roster: append([]activitypresentation.AgentRow(nil), frame.Agents.Roster...)}
	for i, kind := range []string{"progress", "reasoning"} {
		frame.Members[i].Status = threadcontrol.Working
		item := activitypresentation.ItemContent{Facts: activitypresentation.Facts{Kind: kind, Text: "Connecting to provider"}}
		if i == 1 {
			item.Text = "**Checking child callers**\n\nThe expanded reasoning body stays in the transcript."
		}
		agent := &frame.Agents.Roster[i].Agent
		agent.Responding = true
		agent.Summary = []activitypresentation.Entry{{Facts: item.Facts, Values: item.Values()}}
	}
	_, ok := alpha.sessions.Load("child")
	if !ok {
		t.Fatal("child runtime publication unavailable")
	}
	childActivity := frame.Agents.Roster[1].Agent
	childActivity.Name = "/root"
	if err := alpha.publishFrame(sessionapi.Frame{FamilyRoot: "same-thread",
		Session: sessionapi.Session{Thread: "child", Caller: "/root/worker", Role: "worker", Workspace: alpha.workdir},
		Input:   &sessionapi.Input{Stoppable: true}, Agents: &activitypresentation.Publication{Roster: []activitypresentation.AgentRow{{Agent: childActivity}}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := alpha.publishFrame(frame); err != nil {
		t.Fatal(err)
	}
	run("wait", "--fn", `document.querySelector('[aria-label="Thread panel"]')?.textContent.includes('Connecting to provider') && document.querySelector('[aria-label="Thread panel"]')?.textContent.includes('Checking child callers')`)
	if artifacts := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); artifacts != "" {
		run("screenshot", filepath.Join(artifacts, "agent-roster-activity.png"))
	}
	// Main follows admitted child source without broadening the child's own view.
	childValue, _ := alpha.frames.Load("child")
	childSource := childValue.(sessionapi.Frame)
	childSource.Live = []preview.Preview{{ID: "family-edit", Thread: "child", Caller: "/root/worker", Workspace: alpha.workdir,
		Tool: "apply_patch", Files: []diffmodel.ReviewFile{diffmodel.RenderReviewFile("", filepath.Join(alpha.workdir, "live.go"), "", "child_source_before\n")}}}
	publishChildLive := func() {
		t.Helper()
		if err := alpha.publishFrame(childSource); err != nil {
			t.Fatal(err)
		}
		if err := alpha.publishFrame(frame); err != nil {
			t.Fatal(err)
		}
	}
	publishChildLive()
	run("wait", "--fn", `(() => { const live = document.querySelector('[aria-label="Live edits"]'); return live?.textContent.includes('child_source_before') && live.textContent.includes('worker'); })()`)
	childSource.Live = slices.Clone(childSource.Live)
	childSource.Live[0].Files = []diffmodel.ReviewFile{diffmodel.RenderReviewFile("", filepath.Join(alpha.workdir, "live.go"), "", "child_source_after\n")}
	publishChildLive()
	run("wait", "--fn", `(() => { const live = document.querySelector('[aria-label="Live edits"]'); return live?.textContent.includes('child_source_after') && !live.textContent.includes('child_source_before'); })()`)
	childSource.Live = nil
	publishChildLive()
	run("wait", "--fn", `!document.querySelector('[aria-label="Live edits"]')`)
	// Agent selection attaches its own runtime while keeping Main's draft and
	// mutation target. Retained-only agents never borrow Main's composer.
	review := changes.ReviewView{Workspace: alpha.workdir, Revision: 1}
	review.Publication = review.Publication.With(changes.GroupCaptures([]changes.Chunk{{
		Key: "child-capture", Workspace: alpha.workdir, CaptureOrder: 1,
		Origin: changes.Origin{Caller: "/root/worker", Change: "child1", Source: "apply_patch"},
		Review: diffmodel.RenderReviewFile("", filepath.Join(alpha.workdir, "child.go"), "", "package child\n// child saved capture\n"),
	}}))
	frame.Review = &review
	frame.Prompts = &sessionapi.Prompts{Questions: []sessionapi.QuestionCall{{ID: "parent-question", Questions: []sessionapi.Question{{ID: "q", Text: "Parent-only question"}}}}}
	if err := alpha.publishFrame(frame); err != nil {
		t.Fatal(err)
	}
	run("fill", "#composer-text", "main draft")
	run("find", "text", "worker", "click", "--exact")
	var attachment pendingAttachment
	select {
	case attachment = <-childAttachment:
	case <-ctx.Done():
		t.Fatal("child attachment was not requested")
	}
	run("fill", "#composer-text", "draft while attaching")
	if got := run("eval", "document.querySelector('#composer-text')?.value"); !strings.Contains(got, "draft while attaching") {
		t.Fatalf("destination draft was not editable during attachment: %s", got)
	}
	run("wait", "--fn", "!document.body.innerText.includes('Parent-only question') && !document.querySelector('button[aria-label=Details]')")
	thread := sessionpresentation.Thread{ID: "child", Caller: "/root"}
	childFrame := sessionapi.NativeFrame{View: sessionpresentation.View{
		Version: sessionpresentation.Version{Attachment: 1, Revision: 1}, Main: thread, Input: thread, Displayed: thread,
		Info: sessionpresentation.MainInfo{Workspace: alpha.workdir, Caller: "/root/worker"},
	}}
	childFrame.Family, _ = alpha.hub.Family("child")
	for i := range 20 {
		childFrame.Processes = append(childFrame.Processes, sessionpresentation.Process{Thread: "child", Turn: "process-turn", Item: fmt.Sprint(i), ID: fmt.Sprint(i), Agent: "/root/controls-links/controls-ui/accessibility-review", Command: fmt.Sprintf("dense %d", i)})
	}
	if err := attachment.native.Publish(attachment.request.Attachment, childFrame); err != nil {
		t.Fatal(err)
	}
	attachment.request.Reply <- ""
	run("wait", "--fn", "document.querySelector('#composer-text')?.value === 'draft while attaching' && !document.querySelector('button[aria-label=Stop]')?.disabled")
	run("wait", "--fn", "document.querySelector('#composer-text')?.placeholder === \"Steer worker's turn\" && document.querySelector('button[aria-label=Stop]')?.title === \"Stop worker's turn\"")
	button("Changes")
	run("wait", "--fn", `document.querySelector('[aria-labelledby="saved-heading"]')?.textContent.includes('child saved capture')`)
	run("find", "role", "tab", "click", "--name", "Agents", "--exact")
	// A child's own feed resolves images against that runtime, not Main's
	// unrelated sequence namespace.
	_, ok = alpha.sessions.Load("child")
	if !ok {
		t.Fatal("child runtime publication unavailable")
	}
	const pixel = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRZkAAAAASUVORK5CYII="
	if err := alpha.publishFrame(sessionapi.Frame{FamilyRoot: "same-thread", Session: sessionapi.Session{Thread: "child", Caller: "/root/worker", Role: "worker", Workspace: alpha.workdir}, Input: &sessionapi.Input{Stoppable: true}, Main: &activitypresentation.Publication{Entries: []activitypresentation.Entry{{Facts: activitypresentation.Facts{Seq: 97, Agent: "You", Kind: "text", Text: "Child image\n\n[Child file](child.md)", Images: []string{pixel}}}}}}); err != nil {
		t.Fatal(err)
	}
	// The process dock can leave this lazy image above the transcript scrollport.
	run("wait", "--fn", "!!document.querySelector('figure')")
	run("eval", "document.querySelector('figure')?.scrollIntoView({block:'center'})")
	run("wait", "--fn", "document.querySelector('img[alt=\"Attached image 1\"]')?.naturalWidth === 1")
	generated := activitypresentation.ItemContent{Facts: activitypresentation.Facts{Seq: 98, Agent: "Main", Kind: "tool", Text: "Generated `artwork.png`", Images: []string{"data:image/png;base64," + base64.StdEncoding.EncodeToString(previewFixturePNG(t))}}}
	if err := alpha.publishFrame(sessionapi.Frame{FamilyRoot: "same-thread", Session: sessionapi.Session{Thread: "child", Caller: "/root/worker", Role: "worker", Workspace: alpha.workdir}, Input: &sessionapi.Input{Stoppable: true}, Main: &activitypresentation.Publication{Entries: []activitypresentation.Entry{
		{Facts: activitypresentation.Facts{Seq: 97, Agent: "You", Kind: "text", Text: "Child image\n\n[Child file](child.md)", Images: []string{pixel}}},
		{Facts: generated.Facts, Values: generated.Values()},
	}}}); err != nil {
		t.Fatal(err)
	}
	run("wait", "--text", "artwork.png")
	run("eval", "Array.from(document.querySelectorAll('figure')).at(-1)?.scrollIntoView({block:'center'})")
	run("wait", "--fn", "document.querySelector('img[alt=\"Generated image artwork.png\"]')?.naturalWidth === 160")
	if artifacts := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); artifacts != "" {
		run("screenshot", filepath.Join(artifacts, "generated-image.png"))
	}
	run("find", "role", "link", "click", "--name", "Child file", "--exact")
	run("wait", "--text", "Child file target reached its runtime.")
	select {
	case request := <-childFile:
		details := request.Operation.Details
		if request.Thread != "child" || details.Displayed.ID != "child" || details.Agent || details.Path != "child.md" {
			t.Fatal("local child file link borrowed Main's destination")
		}
	case <-ctx.Done():
		t.Fatal("child file link did not reach its runtime")
	}

	run("set", "viewport", "390", "844")
	run("wait", "--fn", `document.querySelector('[aria-label="Active processes"] code')?.getBoundingClientRect().width > 60`)
	button("Stop dense 0")
	select {
	case stopped := <-processStop:
		if stopped.request.Thread != "child" || stopped.request.Operation.StopProcess.ID != "0" {
			t.Fatal("process Stop lost its child destination")
		}
		stopped.request.Reply <- ""
		childFrame.View.Version.Revision++
		childFrame.View.Status.Feedback.Text, childFrame.View.Status.Feedback.Alert = "Could not stop process: dense fixture refusal", true
		if err := stopped.native.Publish(stopped.request.Attachment, childFrame); err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("process Stop did not reach the selected child")
	}
	run("wait", "--fn", `(() => { const note = document.querySelector('[aria-label="Thread feedback"]'); const command = document.querySelector('button[aria-label="Stop dense 0"]'); if (!note || !command || !note.textContent.includes('dense fixture refusal')) return false; const n=note.getBoundingClientRect(), c=command.getBoundingClientRect(); return n.top >= 0 && n.bottom <= innerHeight && c.top >= 0 && c.bottom <= innerHeight; })()`)
	if artifacts := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); artifacts != "" {
		run("screenshot", filepath.Join(artifacts, "process-feedback-phone.png"))
	}
	childFrame.Processes = nil
	childFrame.View.Status.Feedback.Text = ""
	childFrame.View.Version.Revision++
	if err := attachment.native.Publish(attachment.request.Attachment, childFrame); err != nil {
		t.Fatal(err)
	}
	run("set", "viewport", "1280", "720")
	restoredReplies := newAppServerSessionTestUI(t, t.TempDir())
	for i, text := range []string{"## Summary\nNested review complete.\n\n[evidence]: https://example.com/evidence\n\n## Details\nNested retained details. See [evidence].", strings.Repeat("Nested unstructured reply paragraph.\n\n", 12)} {
		entry := nativeDeliveryEntry(threadcontrol.NativeInput{Thread: "main", Sender: "/root/worker/reviewer", Recipient: "/root", Kind: "MESSAGE", Message: text}, fmt.Sprint(i), time.Now())
		applyRestoredMain(&restoredReplies.attached, []*restoredPlacement{{entry: entry}})
	}
	if err := alpha.publishFrame(sessionapi.Frame{FamilyRoot: "same-thread", Session: sessionapi.Session{Thread: "child", Caller: "/root/worker", Role: "worker", Workspace: alpha.workdir}, Input: &sessionapi.Input{Stoppable: true}, ChildReplySummary: true, Main: restoredReplies.attached.source.Publication(restoredReplies.attached.presentation, nil)}); err != nil {
		t.Fatal(err)
	}
	run("wait", "--fn", `(() => { const feed = document.querySelector('[aria-label="worker\'s activity"]'); return feed?.textContent.includes('Nested review complete.') && [...feed.querySelectorAll('summary')].some(e => e.textContent === 'Details') && [...feed.querySelectorAll('button')].some(e => e.textContent.trim() === 'Show full reply'); })()`)
	run("find", "first", `[data-entry]:has([data-kind="reply"]) button[aria-label="Copy response"]`, "click")
	run("wait", "--fn", `window.copiedResponse === '## Summary\nNested review complete.\n\n[evidence]: https://example.com/evidence\n\n## Details\nNested retained details. See [evidence].'`)
	run("eval", `document.querySelector('[data-kind="reply"] details').open = true`)
	run("wait", "--fn", `(() => { const row = document.querySelector('[data-kind="reply"]'); return row.innerText.includes('Nested retained details.') && row.innerText.split('Nested review complete.').length === 2 && !row.querySelector('h2') && row.querySelector('details a[href="https://example.com/evidence"]'); })()`)
	run("wait", "--fn", `(() => { const row = document.querySelector('[data-kind="reply"]'); return row?.textContent.includes('worker/reviewer') && !row.textContent.includes('/root/') && /→\s*worker/.test(row.textContent) && !/→\s*main/.test(row.textContent); })()`)
	if artifacts := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); artifacts != "" {
		run("screenshot", filepath.Join(artifacts, "child-incoming-replies.png"))
	}
	run("fill", "#composer-text", "child draft")
	button("Stop")
	select {
	case intent := <-alpha.intents:
		if intent.Thread != "child" {
			t.Fatalf("child Stop targeted %q", intent.Thread)
		}
	case <-ctx.Done():
		t.Fatal("child Stop did not reach its runtime")
	}
	button("Back to main")
	run("wait", "--fn", "document.querySelector('#composer-text')?.value === 'main draft'")
	run("find", "text", "worker", "click", "--exact")
	run("wait", "--fn", "document.querySelector('#composer-text')?.value === 'child draft'")
	button("Back to main")
	// An input accepted by Main's owner remains pending there while its view is
	// off-screen. Agent navigation never restores it as a failed submission.
	run("wait", "--fn", "document.querySelector('#composer-text')?.value === 'main draft'")
	run("fill", "#composer-text", "pending main input")
	button("Send")
	var reply chan string
	select {
	case reply = <-inputReply:
	case <-time.After(5 * time.Second):
		t.Fatalf("Main input was not admitted: %s", run("snapshot", "-i"))
	}
	run("find", "text", "worker", "click", "--exact")
	button("Back to main")
	run("wait", "--fn", "document.querySelector('#composer-text')?.value === '' && document.querySelector('#composer-text')?.readOnly")
	reply <- "known admission refusal"
	run("wait", "--fn", "document.querySelector('#composer-text')?.value === 'pending main input' && !document.querySelector('#composer-text')?.readOnly")
	run("wait", "--text", "known admission refusal")
	// A setting's delayed menu dismissal must not continue on the thread selected
	// in the meantime. Staying on the originating thread still applies the action.
	for _, action := range []struct{ title, command string }{{"Enable fast mode", "/tier priority"}, {"Reasoning effort", "/effort"}} {
		for _, navigate := range []bool{false, true} {
			run("click", "button[title=Model]")
			select {
			case request := <-settingsCommands:
				if request.Thread != "same-thread" || *request.Operation.Command != "/model" {
					t.Fatalf("opening model settings targeted %s: %s", request.Thread, *request.Operation.Command)
				}
			case <-ctx.Done():
				t.Fatal("model settings did not reach Main")
			}
			run("wait", "--fn", `!!document.querySelector('[role=listbox][aria-label=Model]') && document.querySelector('button[title="Enable fast mode"]')?.disabled === false`)
			holdSettingsDismissal.Store(true)
			run("click", fmt.Sprintf("button[title=%q]", action.title))
			var dismissal sessionapi.NativeRequest
			select {
			case dismissal = <-settingsDismissal:
				if dismissal.Thread != "same-thread" {
					t.Fatal("settings dismissal lost its originating thread")
				}
			case <-ctx.Done():
				t.Fatal("settings transition did not dismiss its menu")
			}
			if navigate {
				run("find", "text", "worker", "click", "--exact")
				run("wait", "--fn", `document.querySelector('#composer-text')?.placeholder === "Steer worker's turn"`)
			}
			dismissal.Reply <- ""
			timeout := 5 * time.Second
			if navigate {
				timeout = 200 * time.Millisecond
			}
			select {
			case request := <-settingsCommands:
				if navigate || request.Thread != "same-thread" || *request.Operation.Command != action.command {
					t.Fatalf("settings continuation after navigation=%v targeted %s: %s", navigate, request.Thread, *request.Operation.Command)
				}
			case <-time.After(timeout):
				if !navigate {
					t.Fatalf("same-thread settings continuation %s was lost", action.command)
				}
			}
			if navigate {
				button("Back to main")
			}
			run("wait", "--fn", `document.querySelector('button[title="Enable fast mode"]')?.getAttribute('aria-busy') === 'false'`)
		}
	}
	run("find", "text", "history", "click", "--exact")
	run("wait", "--text", "This agent's retained activity is read-only.")
	button("Back to main")
	interrupt(alpha, beta)
	alphaAddress := run("get", "url")
	add(beta, "Beta", strings.Repeat("b", 64))
	interrupt(beta, alpha)
	betaAddress := run("get", "url")
	if alphaAddress == betaAddress || !strings.Contains(alphaAddress, "same-thread") || !strings.Contains(betaAddress, "same-thread") {
		t.Fatal("equal thread IDs did not retain distinct machine addresses")
	}
	reload(betaAddress)
	interrupt(beta, alpha)

	open("Alpha")
	interrupt(alpha, beta)
	if err := alpha.bridge.Load().Close(); err != nil {
		t.Fatal(err)
	}
	run("wait", "--text", "Cannot reach this machine")
	run("wait", "--text", "Unreachable, retrying")
	open("Beta")
	interrupt(beta, alpha)
	alpha.restart(t)
	alpha.publish(t, "Alpha recovered")
	open("Alpha")
	run("wait", "--text", "Alpha recovered")
	interrupt(alpha, beta)

	if err := alpha.bridge.Load().Rotate(); err != nil {
		t.Fatal(err)
	}
	run("wait", "--text", "valid bearer token")
	run("wait", "--text", "Token rejected")
	open("Beta")
	interrupt(beta, alpha)
	button("Machines")
	button("Replace token for Alpha")
	token, err := os.ReadFile(alpha.options.TokenFile)
	if err != nil {
		t.Fatal(err)
	}
	run("find", "label", "Access token", "fill", strings.TrimSpace(string(token)))
	button("Reconnect")
	run("wait", "--fn", "Array.from(document.querySelectorAll('dialog[open] li')).some(row => row.textContent.includes('Alpha') && row.textContent.includes('Connected'))")
	closeDialog()
	open("Alpha")
	run("wait", "--text", "Alpha recovered")
	interrupt(alpha, beta)
	reload(alphaAddress)
	run("wait", "--text", "Alpha recovered")
	interrupt(alpha, beta)

	button("Machines")
	button("Remove Alpha")
	button("Remove")
	run("wait", "--fn", "!Array.from(document.querySelectorAll('dialog[open] li')).some(row => row.textContent.includes('Alpha'))")
	closeDialog()
	open("Beta")
	interrupt(beta, alpha)
	reload(betaAddress)
	button("Machines")
	run("wait", "--fn", "!Array.from(document.querySelectorAll('dialog[open] li')).some(row => row.textContent.includes('Alpha'))")
	closeDialog()
	interrupt(beta, alpha)
	add(alpha, "Alpha", strings.TrimSpace(string(token)))
	run("wait", "--text", "Alpha recovered")
	interrupt(alpha, beta)

	run("eval", "Storage.prototype.setItem = Storage.prototype.removeItem = () => { throw new Error('fixture storage failure'); }")
	button("Machines")
	button("Replace token for Alpha")
	run("find", "label", "Access token", "fill", strings.TrimSpace(string(token)))
	button("Reconnect")
	run("wait", "--fn", "document.querySelector('dialog[open] [role=alert]')?.textContent.includes('Reloading may restore older machines or tokens')")
	run("wait", "--fn", "Array.from(document.querySelectorAll('dialog[open] li')).some(row => row.textContent.includes('Alpha') && row.textContent.includes('Connected'))")
	button("Remove Alpha")
	button("Remove")
	run("wait", "--fn", "!Array.from(document.querySelectorAll('dialog[open] li')).some(row => row.textContent.includes('Alpha')) && document.querySelector('dialog[open] [role=alert]')?.textContent.includes('Reloading may restore older machines or tokens')")
	closeDialog()
	run("wait", "--fn", "document.querySelector('[role=alert]')?.textContent.includes('Reloading may restore older machines or tokens')")
	open("Beta")
	interrupt(beta, alpha)
	t.Log("Built UI passed two-machine identity, routing, reconnect, reload persistence, token replacement, and disconnect acceptance")
}

type webUIBrowserMachine struct {
	hub          *sessionapi.Hub
	frames       sync.Map
	sessions     sync.Map
	childThreads []string
	api          *sessionapi.Server
	remote       *httptest.Server
	bridge       atomic.Pointer[connector.Connector]
	options      connector.Options
	intents      chan sessionapi.Intent
	workdir      string
	start        func(string)
}

func (m *webUIBrowserMachine) publishFrame(frame sessionapi.Frame) error {
	m.frames.Store(frame.Session.Thread, frame)
	server, ok := m.sessions.Load(frame.Session.Thread)
	if !ok {
		return fmt.Errorf("fixture runtime %s is unavailable", frame.Session.Thread)
	}
	return server.(*sessionapi.Server).Publish(frame)
}

func newWebUIBrowserMachine(t *testing.T, ctx context.Context, origin, label, token string) *webUIBrowserMachine {
	t.Helper()
	return newWebUIBrowserMachineWithNative(t, ctx, origin, label, token, nil)
}

func newWebUIBrowserMachineWithNative(t *testing.T, ctx context.Context, origin, label, token string, handle func(*sessionapi.Native, sessionapi.NativeRequest) bool) *webUIBrowserMachine {
	t.Helper()
	directory, err := os.MkdirTemp("", "mekugi-webui-browser-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	machine := &webUIBrowserMachine{intents: make(chan sessionapi.Intent, 16), workdir: directory}
	launcher := &nativeReattachLauncher{prepare: func(_ context.Context, _ string, input sessionapi.InputRequest) (*sessionapi.PreparedInput, error) {
		return &sessionapi.PreparedInput{Draft: composer.Draft{Text: input.Text}}, nil
	}, run: func(ctx context.Context, start sessionapi.Start, api *sessionapi.Server) error {
		machine.sessions.Store(start.Resume, api)
		if start.Resume == "same-thread" {
			machine.api = api
		}
		title := label + " thread"
		if start.Resume != "same-thread" {
			title = label + " " + start.Resume + " thread"
		}
		familyRoot, caller, role := start.Resume, "/root", ""
		if slices.Contains(machine.childThreads, start.Resume) {
			familyRoot, caller, role = "same-thread", "/root/worker", "worker"
		}
		if err := machine.publishFrame(sessionapi.Frame{FamilyRoot: familyRoot, Session: sessionapi.Session{Thread: start.Resume, Caller: caller, Role: role, Title: title, Workspace: directory}, Input: &sessionapi.Input{Stoppable: true}}); err != nil {
			return err
		}
		if err := api.SetSummary(sessionapi.Summary{Thread: start.Resume, Title: title, Workspace: directory, Ready: true}); err != nil {
			return err
		}
		native := api.Native()
		for {
			select {
			case intent := <-api.Intents():
				machine.intents <- intent
				intent.Reply <- ""
			case request := <-native.Requests():
				if request.Attach {
					if raw, ok := machine.frames.Load(start.Resume); ok {
						if err := api.Publish(raw.(sessionapi.Frame)); err != nil {
							request.Reply <- err.Error()
							continue
						}
					}
				}
				if handle != nil && handle(native, request) {
					continue
				}
				if request.Attach {
					thread := sessionpresentation.Thread{ID: start.Resume, Caller: "/root"}
					family, _ := machine.hub.Family(start.Resume)
					frame := sessionapi.NativeFrame{Family: family, View: sessionpresentation.View{
						Version: sessionpresentation.Version{Attachment: 1, Revision: 1},
						Main:    thread, Input: thread, Displayed: thread,
						Info: sessionpresentation.MainInfo{Workspace: directory},
					}}
					if raw, ok := machine.frames.Load(start.Resume); ok && raw.(sessionapi.Frame).Prompts != nil {
						for _, call := range raw.(sessionapi.Frame).Prompts.Questions {
							frame.Questions = append(frame.Questions, prompt.CallView{ID: call.ID, Thread: start.Resume})
						}
					}
					if err := native.Publish(request.Attachment, frame); err != nil {
						request.Reply <- err.Error()
						continue
					}
				}
				request.Reply <- ""
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}}
	listener, err := sessionapi.ListenControl(filepath.Join(directory, "control.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	hub := sessionapi.NewHub(ctx, launcher)
	machine.hub = hub
	t.Cleanup(hub.Close)
	server := &http.Server{Handler: sessionapi.Versioned(hub.Handler())}
	t.Cleanup(func() { _ = server.Close() })
	go func() { _ = server.Serve(listener) }()
	transport := sessionapi.ControlTransport(filepath.Join(directory, "control.sock"))
	defer transport.CloseIdleConnections()
	rpc, err := sessionapi.OpenRPC(ctx, &http.Client{Transport: transport}, "http://mekugi"+sessionapi.RPCPath, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rpc.Close() })
	machine.start = func(thread string) {
		t.Helper()
		start := sessionapi.Start{Workspace: directory, Model: "fixture", Resume: thread}
		if slices.Contains(machine.childThreads, thread) {
			if _, err := hub.StartPrepared(ctx, "same-thread", thread, directory, func(ctx context.Context, api *sessionapi.Server) error { return launcher.run(ctx, start, api) }); err != nil {
				t.Fatal(err)
			}
			return
		}
		serverRPCCall(t, rpc, "session/start", "", start)
	}
	machine.start("same-thread")
	machine.options = connector.Options{SocketPath: filepath.Join(directory, "control.sock"), TokenFile: filepath.Join(directory, "token"), Origins: []string{origin}}
	if err := os.WriteFile(machine.options.TokenFile, []byte(token+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	machine.restart(t)
	t.Cleanup(func() { _ = machine.bridge.Load().Close() })
	machine.remote = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { machine.bridge.Load().ServeHTTP(w, r) }))
	t.Cleanup(machine.remote.Close)
	return machine
}

func (m *webUIBrowserMachine) restart(t *testing.T) {
	t.Helper()
	bridge, err := connector.New(m.options)
	if err != nil {
		t.Fatal(err)
	}
	m.bridge.Store(bridge)
}

func (m *webUIBrowserMachine) publish(t *testing.T, title string) {
	t.Helper()
	if err := m.publishFrame(sessionapi.Frame{FamilyRoot: "same-thread", Session: sessionapi.Session{Thread: "same-thread", Caller: "/root", Title: title, Workspace: m.workdir}, Input: &sessionapi.Input{Stoppable: true}}); err != nil {
		t.Fatal(err)
	}
}
