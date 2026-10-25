package launch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/connector"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/webui"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
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
	alpha := newWebUIBrowserMachine(t, ctx, page.URL, "Alpha", strings.Repeat("a", 64))
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
			errors, _ := exec.CommandContext(ctx, browser, "--session", name, "errors").CombinedOutput()
			t.Fatalf("browser %s failed: %v: %s\n%s\n%s", strings.Join(args[:min(2, len(args))], " "), err, output, snapshot, errors)
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
	// Captured temporary trees fold at the same boundary as the terminal;
	// opening them reveals retained paths without another host request.
	var captures []string
	for i := range 200 {
		captures = append(captures, fmt.Sprintf("Create `/tmp/tree/file%03d.go` +3 -0 · cp", i))
	}
	item := activitypresentation.ItemContent{Facts: activitypresentation.Facts{Seq: 1, Agent: "Main", Kind: "tool", Text: strings.Join(captures, "\n\n")}}
	frame := sessionapi.Frame{Session: sessionapi.Session{Thread: "same-thread", Title: "Alpha thread", Workspace: alpha.workdir}, Input: &sessionapi.Input{Stoppable: true},
		Main: &activitypresentation.Publication{Entries: []activitypresentation.Entry{{Facts: item.Facts, Values: item.Values(), Revision: 1}}}}
	if err := alpha.api.Publish(frame); err != nil {
		t.Fatal(err)
	}
	run("wait", "--text", "188 more files")
	if run("eval", "document.body.innerText.includes('file199.go')") != "false\n" {
		t.Fatal("large temporary capture started expanded")
	}
	run("find", "text", "188 more files", "click", "--exact")
	run("wait", "--fn", "document.body.innerText.includes('file199.go')")
	frame.Main.Entries[0].Revision++
	if err := alpha.api.Publish(frame); err != nil {
		t.Fatal(err)
	}
	run("wait", "--fn", "document.body.innerText.includes('file199.go')")
	run("find", "text", "188 more files", "click", "--exact")
	run("wait", "--fn", "!document.body.innerText.includes('file199.go')")
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
	api     *sessionapi.Server
	remote  *httptest.Server
	bridge  atomic.Pointer[connector.Connector]
	options connector.Options
	intents chan sessionapi.Intent
	workdir string
	start   func(string)
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
	launcher := &nativeReattachLauncher{run: func(ctx context.Context, start sessionapi.Start, api *sessionapi.Server) error {
		if start.Resume == "same-thread" {
			machine.api = api
		}
		title := label + " thread"
		if start.Resume != "same-thread" {
			title = label + " " + start.Resume + " thread"
		}
		if err := api.Publish(sessionapi.Frame{Session: sessionapi.Session{Thread: start.Resume, Title: title, Workspace: directory}, Input: &sessionapi.Input{Stoppable: true}}); err != nil {
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
				if handle != nil && handle(native, request) {
					continue
				}
				if request.Attach {
					thread := sessionpresentation.Thread{ID: start.Resume, Caller: "/root"}
					frame := sessionapi.NativeFrame{View: sessionpresentation.View{
						Version: sessionpresentation.Version{Attachment: 1, Revision: 1},
						Main:    thread, Input: thread, Displayed: thread,
						Info: sessionpresentation.MainInfo{Workspace: directory},
					}}
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
		serverRPCCall(t, rpc, "session/start", "", sessionapi.Start{Workspace: directory, Model: "fixture", Resume: thread})
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
	if err := m.api.Publish(sessionapi.Frame{Session: sessionapi.Session{Thread: "same-thread", Title: title, Workspace: m.workdir}, Input: &sessionapi.Input{Stoppable: true}}); err != nil {
		t.Fatal(err)
	}
}
