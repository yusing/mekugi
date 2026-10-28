package launch

import (
	"context"
	"encoding/base64"
	json "encoding/json/v2"
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
	var holdRename atomic.Bool
	renameReply := make(chan chan string, 1)
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
			} else if command != nil && strings.HasPrefix(*command, "/title ") && holdRename.Swap(false) {
				renameReply <- request.Reply
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
	alpha.childThreads = []string{"child", "cold"}
	alpha.start("child")
	coldReady := make(chan struct{})
	alpha.api.BindChildAttachment(func(ctx context.Context, owner, target string) error {
		if owner != "same-thread" || target != "cold" {
			return fmt.Errorf("unexpected cold target %s from %s", target, owner)
		}
		select {
		case <-coldReady:
		case <-ctx.Done():
			return ctx.Err()
		}
		alpha.start(target)
		return nil
	})
	rootBeforeCold, _ := alpha.frames.Load("same-thread")
	coldFamily := rootBeforeCold.(sessionapi.Frame)
	coldFamily.Members = []threadcontrol.Participant{{Thread: "same-thread", Task: "/root"}, {Thread: "cold", Parent: "same-thread", Task: "/root/cold"}}
	if err := alpha.publishFrame(coldFamily); err != nil {
		t.Fatal(err)
	}
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
	// The theme button moves through System, Light and Dark in turn.
	theme := func(name string) {
		t.Helper()
		for range 3 {
			if strings.TrimSpace(run("eval", `document.querySelector('[aria-label="Theme: `+name+`"]') !== null`)) == "true" {
				return
			}
			run("eval", `document.querySelector('[aria-label^="Theme: "]').click()`)
		}
		t.Fatalf("theme button never reached %s", name)
	}
	closeDialog := func() {
		t.Helper()
		button("Close dialog")
		run("wait", "--fn", "!document.querySelector('[role=dialog][data-slot=dialog-content][data-state=open]')")
	}
	// Threads from every machine are links in the sidebar, under their machine.
	firstPublication := true
	open := func(label string) {
		t.Helper()
		run("wait", "--text", label+" thread")
		run("find", "text", label+" thread", "click", "--exact")
		if firstPublication {
			// Hold application delivery, not authentication or the native view.
			// Controls must wait for shared authority even when the view wins.
			run("wait", "--fn", "window.heldShared.length > 0 && window.liveBeforeShared")
			run("eval", `if (document.querySelector('button[aria-label=Stop]')?.disabled === false) throw Error('Controls became actionable before shared live authority'); window.holdShared = false; for (const deliver of window.heldShared.splice(0)) deliver();`)
			firstPublication = false
		}
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
		run("wait", "--fn", "!document.querySelector('[role=dialog][data-slot=dialog-content][data-state=open]')")
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
	run("eval", `(() => {
        const originalFetch = window.fetch;
        window.fetch = async (...args) => {
            const response = await originalFetch(...args);
            if (String(args[0]).endsWith('/api/resources/content') && window.holdContent)
                return new Promise((resolve, reject) => {
                    const signal = args[1]?.signal;
                    if (signal?.aborted) { reject(signal.reason); return; }
                    signal?.addEventListener('abort', () => reject(signal.reason), {once:true});
                    window.heldContent.push((fail = false) => resolve(fail ? new Response(JSON.stringify({code:-32000,message:'content unavailable',data:{status:503}}), {status:503,headers:{'Content-Type':'application/json'}}) : response));
                });
            return response;
        };
		const Original = window.WebSocket;
		window.holdShared = true; window.heldShared = []; window.liveBeforeShared = false;
		window.coldReads = 0; window.coldRefused = false;
		window.WebSocket = class extends Original {
			observations = new Set();
			cold = new Set();
			renames = new Set();
			send(data) {
				const request = JSON.parse(data);
				if (request.params?.body?.operation?.command === '/title Pending parent title') this.renames.add(request.id);
				if (request.method === 'session/subscribe' && request.params.thread) this.observations.add(request.params.subscription);
				if (request.method === 'session/subscribe' && request.params.thread === 'cold') { this.cold.add(request.id); window.coldReads++; }
				return super.send(data);
			}
			addEventListener(kind, listener, ...options) {
				if (kind !== 'message') return super.addEventListener(kind, listener, ...options);
				return super.addEventListener(kind, event => {
					const message = JSON.parse(event.data);
					if (this.renames.has(message.id)) window.pendingRenameSettled = true;

					if (this.cold.has(message.id) && message.error?.data?.status === 404) window.coldRefused = true;
					if (window.holdShared && message.method === 'subscription/event') {
						if (this.observations.has(message.params.subscription)) {
							window.heldShared.push(() => listener.call(this, event));
							return;
						}
						if (message.params.data.frame?.view?.Main?.ID === 'same-thread') window.liveBeforeShared = true;
					}
					listener.call(this, event);
				}, ...options);
			}
		};
	})()`)
	run("snapshot", "-i")
	add(alpha, "Alpha", strings.Repeat("a", 64))
	// Shared controls keep keyboard focus inside a modal and restore its opener.
	button("Machines")
	run("wait", "--fn", `document.querySelector('[data-slot=dialog-content][data-state=open]')?.contains(document.activeElement)`)
	run("press", "Shift+Tab")
	run("wait", "--fn", `document.querySelector('[role=dialog][data-slot=dialog-content][data-state=open]')?.contains(document.activeElement)`)
	run("press", "Escape")
	run("wait", "--fn", `!document.querySelector('[role=dialog][data-slot=dialog-content][data-state=open]') && document.activeElement?.textContent.trim().startsWith('Machines')`)
	// The attachment menu supports keyboard opening, navigation and focus return.
	run("focus", `[aria-label="Add files or paths"]`)
	run("press", "Enter")
	run("wait", "--fn", `document.activeElement?.getAttribute('role') === 'menuitem'`)
	run("eval", `window.firstAttachmentAction = document.activeElement`)
	run("press", "ArrowDown")
	run("wait", "--fn", `document.activeElement?.getAttribute('role') === 'menuitem' && document.activeElement !== window.firstAttachmentAction`)
	run("press", "Escape")
	run("wait", "--fn", `document.activeElement?.getAttribute('aria-label') === 'Add files or paths' && !document.querySelector('[role=menu][data-state=open]')`)
	// An admitted, cold child is absent from discovery. Its successful view
	// attachment must retry the shared read that lost the preparation race.
	run("fill", "#composer-text", "draft before cold child")
	run("eval", `const selection = new URLSearchParams(location.hash.slice(1)); selection.set('thread', 'cold'); location.hash = selection.toString();`)
	run("wait", "--fn", "window.coldRefused")
	if run("eval", `document.body.innerText.includes('No such thread') || document.body.innerText.includes('Thread unavailable')`) != "false\n" {
		t.Fatal("cold preparation was presented as a missing thread")
	}
	close(coldReady)
	run("wait", "--fn", "document.querySelector('button[aria-label=Stop]')?.disabled === false && document.title === 'Alpha cold thread · Mekugi'")
	if run("eval", "window.coldReads") != "2\n" {
		t.Fatal("cold attachment did not make exactly one shared-read retry")
	}
	button("Stop")
	select {
	case intent := <-alpha.intents:
		if intent.Thread != "cold" || intent.Action != sessionapi.Stop {
			t.Fatalf("cold control reached wrong target: %+v", intent)
		}
	case <-ctx.Done():
		t.Fatal("cold child control was not usable")
	}
	open("Alpha")
	if run("get", "value", "#composer-text") != "draft before cold child\n" {
		t.Fatal("cold navigation lost the source draft")
	}
	if err := alpha.publishFrame(rootBeforeCold.(sessionapi.Frame)); err != nil {
		t.Fatal(err)
	}
	// Role assignments accumulate across publications, even when only one child
	// is visible. Exercise the colour cube after the twelve preferred colours.
	beforeRoles, _ := alpha.frames.Load("child")
	for i := 1; i <= 14; i++ {
		role := fmt.Sprintf("role-%02d", i)
		child := beforeRoles.(sessionapi.Frame)
		child.Session.Role = role
		if err := alpha.publishFrame(child); err != nil {
			t.Fatal(err)
		}
		root, _ := alpha.frames.Load("same-thread")
		if err := alpha.publishFrame(root.(sessionapi.Frame)); err != nil {
			t.Fatal(err)
		}
		run("wait", "--text", role)
		if i < 12 {
			continue
		}
		for _, choice := range []string{"Dark", "Light"} {
			theme(choice)
			t.Log(role, choice, run("eval", `(() => {
				const canvas = document.createElement('canvas'), ctx = canvas.getContext('2d');
				const luminance = color => { ctx.fillStyle = color; ctx.fillRect(0, 0, 1, 1); return [...ctx.getImageData(0, 0, 1, 1).data].slice(0, 3).map(c => c / 255).map(c => c <= .04045 ? c / 12.92 : ((c + .055) / 1.055) ** 2.4).reduce((sum, c, i) => sum + c * [.2126, .7152, .0722][i], 0); };
				const labels = [...document.querySelectorAll('[aria-label="Thread panel"] .text-role')];
				if (!labels.some(e => e.textContent.startsWith('role-'))) throw Error('Role labels missing');
				return labels.map(e => { const ink = luminance(getComputedStyle(e).color); let surface = e; while (getComputedStyle(surface).backgroundColor === 'rgba(0, 0, 0, 0)') surface = surface.parentElement; const paper = luminance(getComputedStyle(surface).backgroundColor); const ratio = (Math.max(ink, paper) + .05) / (Math.min(ink, paper) + .05); if (ratio < 4.5) throw Error(e.textContent + ' contrast ' + ratio); return {label:e.textContent, contrast:ratio}; });
			})()`))
			if directory := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); directory != "" {
				run("screenshot", filepath.Join(directory, role+"-"+choice+".png"))
			}
		}
	}
	if err := alpha.publishFrame(beforeRoles.(sessionapi.Frame)); err != nil {
		t.Fatal(err)
	}
	rootAfterRoles, _ := alpha.frames.Load("same-thread")
	if err := alpha.publishFrame(rootAfterRoles.(sessionapi.Frame)); err != nil {
		t.Fatal(err)
	}
	run("wait", "--text", "worker")
	theme("System")
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
	run("wait", "--fn", "!document.querySelector('[role=dialog][data-slot=dialog-content][data-state=open]')")
	if request := <-browserCommands; *request.Operation.Command != "/title A useful title" || request.Operation.Editor != nil {
		t.Fatal("rename changed the composer")
	}
	// A rename's completion belongs to its original dialog, even after navigation.
	holdRename.Store(true)
	button("Rename thread")
	run("fill", "#thread-title", "Pending parent title")
	button("Save")
	parentRename := <-renameReply
	<-browserCommands
	button("Cancel")
	run("find", "text", "cold", "click", "--exact")
	run("wait", "--fn", `!!document.querySelector('button[aria-label="Rename thread"]') && !document.querySelector('button[aria-label="Rename thread"]').disabled`)
	button("Rename thread")
	run("fill", "#thread-title", "Child title")
	parentRename <- ""
	run("wait", "--fn", "window.pendingRenameSettled")
	run("wait", "--fn", `document.querySelector('#thread-title')?.value === 'Child title' && !document.querySelector('#thread-title').disabled`)
	button("Save")
	run("wait", "--fn", "!document.querySelector('[role=dialog][data-slot=dialog-content][data-state=open]')")
	if request := <-browserCommands; request.Thread != "cold" || *request.Operation.Command != "/title Child title" {
		t.Fatal("rename completion crossed thread dialogs")
	}
	button("Back to main")
	run("wait", "--fn", `document.querySelector('#composer-text')?.value === 'keep draft'`)
	for _, reject := range []bool{false, true} {
		run("eval", `Object.defineProperty(navigator, 'clipboard', {configurable:true, value:{writeText: command => { window.attemptedCopy = command; return new Promise((resolve, reject) => { window.finishCopy = resolve; window.failCopy = reject; }); }}})`)
		button("Session details")
		button("Copy resume command")
		run("wait", "--fn", `window.attemptedCopy === 'mekugi resume same-thread'`)
		run("press", "Escape")
		run("find", "text", "cold", "click", "--exact")
		run("wait", "--fn", `document.title === 'Alpha cold thread · Mekugi'`)
		if reject {
			run("eval", `window.failCopy(new Error('Clipboard denied')); new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))`)
		} else {
			run("eval", `window.finishCopy(); new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))`)
		}
		button("Session details")
		if run("eval", `document.querySelector('button[aria-label="Copy resume command"]')?.title === 'Copy' && !document.querySelector('[role=dialog][data-slot=dialog-content][data-state=open]')`) != "true\n" {
			t.Fatal("clipboard completion crossed thread navigation")
		}
		run("press", "Escape")
		button("Back to main")
		run("wait", "--fn", `document.querySelector('#composer-text')?.value === 'keep draft'`)
	}
	responseMarkdown := "Response with **authored Markdown**.\n\n```go\nfmt.Println(\"complete\")\n```\n\n" + strings.Repeat("Oversized prose loads automatically.\n\n", 120) + "End of complete response."
	if err := alpha.publishFrame(sessionapi.Frame{Session: sessionapi.Session{Thread: "same-thread", Caller: "/root", Title: "Alpha thread", Workspace: alpha.workdir}, Input: &sessionapi.Input{Stoppable: true}, Main: &activitypresentation.Publication{Entries: []activitypresentation.Entry{{Facts: activitypresentation.Facts{Seq: 99, Agent: "Main", Kind: "final", Text: responseMarkdown}}}}}); err != nil {
		t.Fatal(err)
	}
	run("wait", "--text", "authored Markdown")
	run("wait", "--text", "End of complete response.")
	run("eval", `Object.defineProperty(navigator, 'clipboard', {configurable:true, value:{writeText: async text => { window.copiedResponse = text; }, write: items => { window.copyWriteActive = navigator.userActivation.isActive; return items[0].getType('text/plain').then(blob => blob.text()).then(text => navigator.clipboard.writeText(text)); }}})`)
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
	// Captured edits keep their semantic row even when the retained raw diff
	// crosses the transport window. The diff belongs in existing Details.
	editItem := activitypresentation.ItemContent{Facts: activitypresentation.Facts{Seq: 97, Agent: "Main", Kind: "tool", Text: "Edit `/tmp/manifest.json` +64 -6 · python3\n```diff\n@@ -1,6 +1,64 @@\n" + strings.Repeat("+  captured_change,\n", 300) + "```"}}
	previewFrame := sessionapi.Frame{Session: sessionapi.Session{Thread: "same-thread", Caller: "/root", Title: "Alpha thread", Workspace: alpha.workdir}, Input: &sessionapi.Input{Stoppable: true}, Main: &activitypresentation.Publication{Entries: []activitypresentation.Entry{{Facts: editItem.Facts, Values: editItem.Values(), Revision: 1}}}}
	if err := alpha.publishFrame(previewFrame); err != nil {
		t.Fatal(err)
	}
	run("wait", "--text", "manifest.json")
	if run("eval", `(() => { const row = document.querySelector('[data-entry="97"]'); return !!row && !row.querySelector('pre') && !!row.querySelector('button[aria-label="Details"]') && !row.textContent.includes('Load complete entry'); })()`) != "true\n" {
		t.Fatal("oversized captured edit lost compact presentation")
	}
	if artifacts := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); artifacts != "" {
		for _, palette := range []string{"Light", "Dark"} {
			run("set", "viewport", "1280", "900")
			theme(palette)
			for _, width := range []string{"1280", "768", "390"} {
				run("set", "viewport", width, "900")
				run("screenshot", filepath.Join(artifacts, "compact-edit-"+strings.ToLower(palette)+"-"+width+".png"))
			}
		}
		run("set", "viewport", "1280", "900")
		theme("System")
	}
	previewFrame.Main = &activitypresentation.Publication{Entries: []activitypresentation.Entry{{Facts: activitypresentation.Facts{Seq: 98, Agent: "Main", Kind: "tool", Text: "Run command"}, Values: []activitypresentation.Value{{Kind: "op", Verb: "Run", Code: "echo begin\n" + strings.Repeat("echo middle\n", 200) + "echo script_finished", Lang: "bash", Fenced: true, Tail: []string{strings.Repeat("output ", 200), "output_finished"}}}, Revision: 1}}}
	if err := alpha.publishFrame(previewFrame); err != nil {
		t.Fatal(err)
	}
	run("wait", "--text", "Script")
	run("wait", "--fn", `!!document.querySelector('[data-entry="98"] button[aria-label="Script: 202 lines"]') && !!document.querySelector('button[aria-label="Output: 2 lines"]')`)
	if run("eval", `document.body.innerText.includes('script_finished') || document.body.innerText.includes('output_finished')`) != "false\n" {
		t.Fatal("deferred command payload started expanded")
	}
	button("Script: 202 lines")
	run("wait", "--text", "script_finished")
	button("Output: 2 lines")
	run("wait", "--text", "output_finished")
	previewFrame.Main = &activitypresentation.Publication{Entries: []activitypresentation.Entry{{Facts: activitypresentation.Facts{Seq: 95, Agent: "Main", Kind: "tool", Text: "JSON arguments"}, Values: []activitypresentation.Value{{Kind: "op", Verb: "Run", Code: "{\n  \"value\": \"" + strings.Repeat("large", 200) + "\",\n  \"message\": \"json_finished\"\n}", Lang: "json", Fenced: true}}, Revision: 2}}}
	if err := alpha.publishFrame(previewFrame); err != nil {
		t.Fatal(err)
	}
	run("wait", "--fn", `!!document.querySelector('[data-entry="95"] button[aria-label="Arguments and result: 4 lines"]')`)
	button("Arguments and result: 4 lines")
	run("wait", "--text", "json_finished")
	// Held independent replies exercise cancellation, late failure, retry and
	// clipboard admission before the complete response is available.
	publishBody := func(revision uint64, text string) {
		t.Helper()
		previewFrame.Main = &activitypresentation.Publication{Entries: []activitypresentation.Entry{{Facts: activitypresentation.Facts{Seq: 96, Agent: "Main", Kind: "final", Text: strings.Repeat("Deferred prose. ", 300) + text}, Revision: revision}}}
		if err := alpha.publishFrame(previewFrame); err != nil {
			t.Fatal(err)
		}
	}
	for _, replacement := range []string{"revision", "attachment"} {
		run("eval", `window.holdContent = true; window.heldContent = []; window.copiedResponse = undefined`)
		publishBody(1, "OLD_CONTENT")
		run("wait", "--fn", `window.heldContent.length === 1`)
		version := uint64(2)
		if replacement == "attachment" {
			previewFrame.Attachment++
			version = 1
		}
		publishBody(version, "NEW_CONTENT")
		run("wait", "--fn", `window.heldContent.length === 2`)
		run("eval", `window.heldContent.shift()(true)`)
		run("wait", "--fn", `!!document.querySelector('[data-entry="96"] button[aria-label="Copy response"]')`)
		run("focus", `[data-entry="96"] button[aria-label="Copy response"]`)
		run("press", "Enter")
		if run("eval", `window.copyWriteActive && window.copiedResponse === undefined && !document.body.innerText.includes('OLD_CONTENT') && !document.body.innerText.includes('content unavailable')`) != "true\n" {
			t.Fatal("stale reply or clipboard gesture escaped its reference")
		}
		run("eval", `window.heldContent.shift()()`)
		run("wait", "--fn", `window.copiedResponse?.endsWith('NEW_CONTENT') && document.body.innerText.includes('NEW_CONTENT') && !document.body.innerText.includes('Loading content')`)
		run("eval", `window.holdContent = false`)
		previewFrame.Attachment++
	}
	run("eval", `window.holdContent = true; window.heldContent = []`)
	publishBody(3, "RETRIED_CONTENT")
	run("wait", "--fn", `window.heldContent.length === 1`)
	run("eval", `window.heldContent.shift()(true)`)
	run("wait", "--text", "content unavailable")
	button("Retry")
	run("wait", "--fn", `window.heldContent.length === 1`)
	run("eval", `window.heldContent.shift()(); window.holdContent = false`)
	run("wait", "--fn", `document.body.innerText.includes('RETRIED_CONTENT') && !document.body.innerText.includes('content unavailable') && !document.body.innerText.includes('Loading content')`)
	// A changing newest entry must not monopolize automatic reads.
	run("eval", `window.holdContent = true; window.heldContent = []`)
	previewFrame.Main = &activitypresentation.Publication{Entries: []activitypresentation.Entry{
		{Facts: activitypresentation.Facts{Seq: 94, Agent: "Main", Kind: "final", Text: strings.Repeat("Earlier prose. ", 300) + "STATIC_CONTENT"}, Revision: 1},
		{Facts: activitypresentation.Facts{Seq: 95, Agent: "Main", Kind: "text", Text: strings.Repeat("Streaming prose. ", 300) + "STREAM_CONTENT"}, Revision: 1},
	}}
	if err := alpha.publishFrame(previewFrame); err != nil {
		t.Fatal(err)
	}
	run("wait", "--fn", `window.heldContent.length === 1`)
	previewFrame.Main = &activitypresentation.Publication{Entries: append([]activitypresentation.Entry(nil), previewFrame.Main.Entries...)}
	previewFrame.Main.Entries[1].Revision++
	if err := alpha.publishFrame(previewFrame); err != nil {
		t.Fatal(err)
	}
	run("wait", "--fn", `window.heldContent.length === 2`)
	run("eval", `window.heldContent.shift()(true); window.heldContent.shift()()`)
	run("wait", "--text", "STATIC_CONTENT")
	run("wait", "--fn", `window.heldContent.length === 1`)
	run("eval", `window.heldContent.shift()(); window.holdContent = false`)
	run("wait", "--text", "STREAM_CONTENT")
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
	run("eval", `window.referencePreviewURLs = []; window.originalCreateObjectURL = URL.createObjectURL; URL.createObjectURL = function(value) { const url = window.originalCreateObjectURL.call(URL, value); window.referencePreviewURLs.push(url); return url; };`)
	run("focus", `button[aria-label="Copy selection to reference"]`)
	run("press", "Enter")
	run("wait", "--fn", `document.querySelector('#composer-text').value.includes('[Selected message]')`)
	if run("eval", `window.referencePreviewURLs.length === 0 && !document.querySelector('[aria-label="Attachments"] img')`) != "true\n" {
		t.Fatal("quoted text reference acquired an image preview")
	}
	run("eval", `URL.createObjectURL = window.originalCreateObjectURL`)
	if request := <-references; request.Operation.Reference.Thread != "same-thread" || !strings.Contains(request.Operation.Reference.Text, "file000.go") {
		t.Fatal("reference lost source or destination")
	}
	if run("get", "value", "#composer-text") != "keep draft[Selected message] \n" {
		t.Fatal("reference replaced the draft")
	}
	run("wait", "--fn", `document.activeElement?.id === "composer-text"`)
	if run("eval", `!document.querySelector('[aria-label="Attachments"]')`) != "true\n" {
		t.Fatal("reference appeared in the attachment tray")
	}
	run("press", "Backspace") // trailing space
	run("press", "Backspace") // atomic marker deletion
	run("wait", "--fn", `document.querySelector('#composer-text').value === 'keep draft'`)
	run("wait", "--fn", `document.querySelector('button[aria-label="Shell command"]').disabled === false`)
	run("press", "Control+z")
	run("wait", "--fn", `document.querySelector('#composer-text').value.includes('[Selected message]')`)
	run("wait", "--fn", `document.querySelector('button[aria-label="Shell command"]').disabled === true`)
	if artifacts := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); artifacts != "" {
		for _, theme := range []string{"light", "dark"} {
			run("eval", fmt.Sprintf(`document.documentElement.dataset.theme = %q`, theme))
			for _, width := range []int{1280, 768, 390} {
				run("set", "viewport", fmt.Sprint(width), "900")
				run("screenshot", filepath.Join(artifacts, fmt.Sprintf("inline-reference-%s-%d.png", theme, width)))
			}
		}
		run("set", "viewport", "1280", "900")
	}
	run("fill", "#composer-text", "keep draft")
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
	run("eval", `window.holdContent = true; window.heldContent = []`)
	run("press", "Enter")
	run("wait", "--fn", `window.heldContent.length === 1`)
	frame.Main = &activitypresentation.Publication{Entries: append([]activitypresentation.Entry(nil), frame.Main.Entries...)}
	frame.Main.Entries[0].Revision++
	if err := alpha.publishFrame(frame); err != nil {
		t.Fatal(err)
	}
	run("wait", "--fn", `window.heldContent.length === 2`)
	run("eval", `window.heldContent.shift()(true); window.heldContent.shift()(); window.holdContent = false`)
	run("wait", "--fn", "document.body.innerText.includes('file199.go')")
	frame.Main = &activitypresentation.Publication{Entries: append([]activitypresentation.Entry(nil), frame.Main.Entries...)}
	frame.Main.Entries[0].Revision++
	if err := alpha.publishFrame(frame); err != nil {
		t.Fatal(err)
	}
	run("wait", "--fn", "document.body.innerText.includes('file199.go')")
	run("eval", `window.holdContent = true; window.heldContent = []`)
	for range 2 {
		frame.Attachment++
		if err := alpha.publishFrame(frame); err != nil {
			t.Fatal(err)
		}
		run("wait", "--fn", fmt.Sprintf(`window.heldContent.length === %d`, frame.Attachment))
	}
	run("eval", `window.heldContent.shift()(true); window.heldContent.shift()(); window.holdContent = false`)
	run("wait", "--fn", `document.body.innerText.includes('file199.go') && !document.body.innerText.includes('content unavailable') && !document.body.innerText.includes('Loading content')`)
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
	frame.Members = append(frame.Members, threadcontrol.Participant{Thread: "native-worker", Parent: "same-thread", Task: "/root/native-worker", NonAttachable: true})
	frame.Main = &activitypresentation.Publication{Entries: []activitypresentation.Entry{
		{Facts: activitypresentation.Facts{Seq: 2, Agent: "Main", Kind: "tool"}, Values: []activitypresentation.Value{{Kind: "progress", Body: "Waiting for", WaitTargets: []activitypresentation.WaitTarget{{Name: "agent:main/worker"}}, WaitTimeout: "5s"}}, Revision: 1},
		{Facts: activitypresentation.Facts{Seq: 3, Agent: "Main", Kind: "tool"}, Values: []activitypresentation.Value{{Kind: "progress", Body: "Finished waiting for", WaitTargets: []activitypresentation.WaitTarget{{Name: "/root/worker"}}, WaitReason: "paused"}}, Revision: 1},
		{Facts: activitypresentation.Facts{Seq: 4, Agent: "Main", Kind: "tool"}, Values: []activitypresentation.Value{{Kind: "progress", Body: "Waiting for", WaitTargets: []activitypresentation.WaitTarget{{Name: "agent:main/one"}, {Name: "agent:main/two"}, {Name: "agent:main/three"}, {Name: "agent:main/four"}, {Name: "agent:main/five"}}}}, Revision: 1},
		{Facts: activitypresentation.Facts{Seq: 5, Agent: "Main", Kind: "tool"}, Values: []activitypresentation.Value{{Kind: "progress", Body: "Waiting for", WaitTargets: []activitypresentation.WaitTarget{{Name: "all"}}}}, Revision: 1},
		{Facts: activitypresentation.Facts{Seq: 6, Agent: "Main", Kind: "reply"}, Values: []activitypresentation.Value{{Kind: "message", From: "agent:main", To: "agent:main/worker", Body: "Review the input."}}, Revision: 1},
		{Facts: activitypresentation.Facts{Seq: 7, Agent: "/root/worker", Kind: "final", Text: "Review complete."}, Revision: 1},
	}}
	for i := range frame.Main.Entries {
		entry := &frame.Main.Entries[i]
		if len(entry.Values) > 0 {
			entry.Text = entry.Values[0].ProgressText()
		}
	}
	if err := alpha.publishFrame(frame); err != nil {
		t.Fatal(err)
	}
	run("wait", "--text", "Waiting for worker (timeout 5s)")
	run("wait", "--fn", `document.querySelector('#panel-content button[title="Interactive view unavailable"]')?.disabled`)
	run("eval", `window.nonattachableRow = document.querySelector('#panel-content button[title="Interactive view unavailable"]'); window.nonattachableURL = location.href; window.nonattachableRow.scrollIntoView({block:'center'}); window.nonattachableRow.click()`)
	coordinates := strings.TrimSpace(run("eval", `(() => { const rect = window.nonattachableRow.getBoundingClientRect(); return Math.round(rect.x + rect.width / 2) + ' ' + Math.round(rect.y + rect.height / 2); })()`))
	point := strings.Fields(strings.Trim(coordinates, `"`))
	if len(point) != 2 {
		t.Fatalf("nonattachable row geometry: %s", coordinates)
	}
	run("mouse", "move", point[0], point[1])
	run("mouse", "down")
	run("mouse", "up")
	run("eval", `document.querySelector('#panel-content button[aria-current=true]').focus(); window.nonattachableRow.focus()`)
	run("press", "Enter")
	if strings.TrimSpace(run("eval", `location.href === window.nonattachableURL && document.activeElement !== window.nonattachableRow && window.nonattachableRow.textContent.includes('native-worker') && !window.nonattachableRow.textContent.includes('tokens') && !window.nonattachableRow.textContent.includes('$')`)) != "true" {
		t.Fatal("nonattachable row navigated, acquired keyboard focus, or invented metrics")
	}
	if artifacts := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); artifacts != "" {
		for _, palette := range []string{"Light", "Dark"} {
			run("set", "viewport", "1280", "900")
			theme(palette)
			for _, width := range []string{"1280", "768", "390"} {
				run("set", "viewport", width, "900")
				run("eval", `if (!document.querySelector('#panel-content')) document.querySelector('button[aria-label^="Agents ("]').click()`)
				run("wait", "--fn", `!!document.querySelector('#panel-content button[title="Interactive view unavailable"]')`)
				run("eval", `window.nonattachableRow = document.querySelector('#panel-content button[title="Interactive view unavailable"]'); window.nonattachableRow.scrollIntoView({block:'center'})`)
				run("wait", "--fn", `(() => { const rect = window.nonattachableRow.getBoundingClientRect(); return rect.left >= 0 && rect.right <= innerWidth && rect.height > 0; })()`)
				run("screenshot", filepath.Join(artifacts, "nonattachable-"+strings.ToLower(palette)+"-"+width+".png"))
			}
		}
		run("set", "viewport", "1280", "900")
		theme("System")
	}
	run("wait", "--text", "Finished waiting for worker: paused")
	// Model-visible coordination addresses and canonical completion identities
	// select the same roster member, retaining its role ink in both themes.
	for _, palette := range []string{"Light", "Dark"} {
		theme(palette)
		run("wait", "--fn", `(() => { const links = [2,3,6,7].map(seq => document.querySelector('[data-entry="'+seq+'"] button[title="Show worker\'s activity"]')); return links.every(link => link?.classList.contains('text-role')) && new Set(links.map(link => getComputedStyle(link).color)).size === 1 && !document.querySelector('[data-entry="6"]').textContent.includes('agent:main'); })()`)
	}
	theme("System")
	run("wait", "--fn", `document.querySelector('[aria-label="Thread panel"] [title="Waiting for worker (timeout 5s)"]')`)
	run("wait", "--text", "1 waiting")
	run("wait", "--text", "Waiting for all")
	run("wait", "--fn", `(() => { const row = document.querySelector('[data-entry="4"]'); return row?.textContent.includes('+3 more') && !row.textContent.includes('three'); })()`)
	run("find", "text", "+3 more", "click", "--exact")
	run("wait", "--text", "Waiting for one, two, three, four, five")
	run("find", "text", "Show fewer agents", "click", "--exact")
	if artifacts := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); artifacts != "" {
		for _, palette := range []string{"Light", "Dark"} {
			run("set", "viewport", "1280", "900")
			theme(palette)
			for _, width := range []string{"1280", "768", "390"} {
				run("set", "viewport", width, "900")
				run("wait", "--fn", `(() => { const button = document.querySelector('[data-entry="4"] button[aria-expanded]'); if (!button) return false; const rect = button.getBoundingClientRect(); return rect.left >= 0 && rect.right <= innerWidth; })()`)
				run("screenshot", filepath.Join(artifacts, "wait-"+strings.ToLower(palette)+"-"+width+".png"))
			}
		}
		run("set", "viewport", "1280", "900")
		theme("System")
	}
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
	run("eval", `window.familyPanel = document.querySelector('[aria-label="Thread panel"]'); window.familyRow = document.querySelector('#panel-content li > button')`)
	run("find", "first", `[data-entry="2"] button[title="Show worker's activity"]`, "click")
	var attachment pendingAttachment
	select {
	case attachment = <-childAttachment:
	case <-ctx.Done():
		t.Fatal("child attachment was not requested")
	}
	if run("eval", `window.familyPanel.isConnected && window.familyRow.isConnected && !document.querySelector('[aria-label="Loading agents"]')`) != "true\n" {
		t.Fatal("agent navigation replaced the family panel or its roster")
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
		entry := nativeDeliveryEntry(threadcontrol.NativeInput{Thread: "main", Sender: "/root/worker/reviewer", Recipient: "/root", Kind: "MESSAGE", Message: text}, fmt.Sprint(i), time.Now(), false)
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
	// A real receiving-runtime publication retained this exact incoming body
	// without a duplicate child-feed entry. Disclosure uses its own content.
	unlinked, original := unlinkedReplyUI(t)
	restoredReplies.attached.source.ApplyMain(unlinked.attached.source.Entries[0].Source)
	if err := alpha.publishFrame(sessionapi.Frame{FamilyRoot: "same-thread", Session: sessionapi.Session{Thread: "child", Caller: "/root/worker", Workspace: alpha.workdir}, Input: &sessionapi.Input{Stoppable: true}, ChildReplySummary: true, Main: restoredReplies.attached.source.Publication(restoredReplies.attached.presentation, nil)}); err != nil {
		t.Fatal(err)
	}
	run("wait", "--fn", `(() => { const row = [...document.querySelectorAll('[data-kind="reply"]')].find(row => row.textContent.includes('Confirmed the separate')); return row?.innerText.includes('Confirmed the separate Loading content… cycle') && !row.innerText.includes('A bounded temporary') && !row.querySelector('h2') && row.querySelector('summary')?.textContent === 'Details'; })()`)
	run("eval", `[...document.querySelectorAll('[data-kind="reply"]')].find(row => row.textContent.includes('Confirmed the separate')).querySelector('details').open = true`)
	run("wait", "--fn", `(() => { const row = [...document.querySelectorAll('[data-kind="reply"]')].find(row => row.textContent.includes('Confirmed the separate')); return row.innerText.includes('A bounded temporary') && row.innerText.split('Confirmed the separate').length === 2 && !row.querySelector('h2'); })()`)
	run("find", "last", `[data-entry]:has([data-kind="reply"]) button[aria-label="Copy response"]`, "click")
	quoted, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	run("wait", "--fn", "window.copiedResponse === "+string(quoted))
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
	run("wait", "--fn", "Array.from(document.querySelectorAll('[role=dialog][data-slot=dialog-content][data-state=open] li')).some(row => row.textContent.includes('Alpha') && row.textContent.includes('Connected'))")
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
	run("wait", "--fn", "!Array.from(document.querySelectorAll('[role=dialog][data-slot=dialog-content][data-state=open] li')).some(row => row.textContent.includes('Alpha'))")
	closeDialog()
	open("Beta")
	interrupt(beta, alpha)
	reload(betaAddress)
	button("Machines")
	run("wait", "--fn", "!Array.from(document.querySelectorAll('[role=dialog][data-slot=dialog-content][data-state=open] li')).some(row => row.textContent.includes('Alpha'))")
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
	run("wait", "--fn", "document.querySelector('[role=dialog][data-slot=dialog-content][data-state=open] [role=alert]')?.textContent.includes('Reloading may restore older machines or tokens')")
	run("wait", "--fn", "Array.from(document.querySelectorAll('[role=dialog][data-slot=dialog-content][data-state=open] li')).some(row => row.textContent.includes('Alpha') && row.textContent.includes('Connected'))")
	button("Remove Alpha")
	button("Remove")
	run("wait", "--fn", "!Array.from(document.querySelectorAll('[role=dialog][data-slot=dialog-content][data-state=open] li')).some(row => row.textContent.includes('Alpha')) && document.querySelector('[role=dialog][data-slot=dialog-content][data-state=open] [role=alert]')?.textContent.includes('Reloading may restore older machines or tokens')")
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
			if start.Resume == "cold" {
				caller = "/root/cold"
			}
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
