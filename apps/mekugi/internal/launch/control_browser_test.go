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
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/connector"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
)

type browserFixtureTool struct {
	executable string
	binPath    string
}

// Set once by TestMain before isolating configuration. Resolution failures only
// fail opted-in browser fixtures, not unrelated tests in the same process.
var browserFixtureExecutable browserFixtureTool
var browserFixtureError error

func browserFixtureCommand(t *testing.T) string {
	t.Helper()
	if browserFixtureError != nil {
		t.Fatal(browserFixtureError)
	}
	if browserFixtureExecutable.binPath != "" {
		t.Setenv("PATH", browserFixtureExecutable.binPath+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	return browserFixtureExecutable.executable
}

func resolveBrowserFixtureExecutable(override string) (browserFixtureTool, error) {
	name := override
	if name == "" {
		name = "agent-browser"
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return browserFixtureTool{}, err
	}
	// An explicit override is authoritative, including a selected manager shim.
	if override != "" {
		return browserFixtureTool{executable: path}, nil
	}
	manager, err := filepath.EvalSymlinks(path)
	if err != nil {
		return browserFixtureTool{}, err
	}
	if filepath.Base(manager) != "mise" && filepath.Base(manager) != "mise.exe" {
		return browserFixtureTool{executable: path}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, manager, "which", "agent-browser")
	output, err := command.Output()
	if err != nil {
		if failure, ok := err.(*exec.ExitError); ok {
			return browserFixtureTool{}, fmt.Errorf("resolve agent-browser with mise: %w: %s", err, failure.Stderr)
		}
		return browserFixtureTool{}, fmt.Errorf("resolve agent-browser with mise: %w", err)
	}
	path, err = exec.LookPath(strings.TrimSpace(string(output)))
	if err != nil {
		return browserFixtureTool{}, err
	}
	// npm wrappers may use env node; retain installed runtime bins too, without
	// carrying host configuration into the isolated fixture.
	bins, err := exec.CommandContext(ctx, manager, "bin-paths").Output()
	if err != nil {
		return browserFixtureTool{}, fmt.Errorf("resolve browser runtime paths with mise: %w", err)
	}
	return browserFixtureTool{executable: path, binPath: strings.Join(strings.Split(strings.TrimSpace(string(bins)), "\n"), string(os.PathListSeparator))}, nil
}

func TestBrowserFixtureExecutable(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("fixture executables use shell scripts and symlinks")
	}
	directory := t.TempDir()
	installed := filepath.Join(directory, "installed-browser")
	if err := os.WriteFile(installed, []byte("#!/usr/bin/env fixture-runtime\n"), 0700); err != nil {
		t.Fatal(err)
	}
	runtimeDirectory := t.TempDir()
	runtime := filepath.Join(runtimeDirectory, "fixture-runtime")
	if err := os.WriteFile(runtime, []byte("#!/bin/sh\nprintf 'installed browser'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	manager := filepath.Join(directory, "mise")
	// The manager must resolve while the host configuration is still available.
	script := fmt.Sprintf("#!/bin/sh\n[ \"$XDG_CONFIG_HOME\" = host-config ] || exit 1\ncase \"$*\" in\n'which agent-browser') printf '%%s\\n' %q ;;\n'bin-paths') printf '%%s\\n' %q ;;\n*) exit 1 ;;\nesac\n", installed, runtimeDirectory)
	if err := os.WriteFile(manager, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(directory, "agent-browser")
	if err := os.Symlink(manager, shim); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(manager, filepath.Join(directory, "fixture-runtime")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	t.Setenv("XDG_CONFIG_HOME", "host-config")
	resolved, err := resolveBrowserFixtureExecutable("")
	if err != nil || resolved.executable != installed {
		t.Fatalf("resolve shim: %q, %v", resolved, err)
	}
	t.Setenv("XDG_CONFIG_HOME", "isolated-config")
	t.Setenv("PATH", resolved.binPath+string(os.PathListSeparator)+os.Getenv("PATH"))
	output, err := exec.CommandContext(t.Context(), resolved.executable).CombinedOutput()
	if err != nil || string(output) != "installed browser" {
		t.Fatalf("run resolved browser after isolation: %v, %s", err, output)
	}
	for _, override := range []string{installed, "installed-browser", shim} {
		got, err := resolveBrowserFixtureExecutable(override)
		want := override
		if override == "installed-browser" {
			want = installed
		}
		if err != nil || got.executable != want {
			t.Fatalf("override %q: got %q, %v; want %q", override, got, err, want)
		}
	}
	if _, err := resolveBrowserFixtureExecutable(""); err == nil {
		t.Fatal("manager resolution failure was suppressed")
	}
	if err := os.Remove(shim); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(installed, shim); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveBrowserFixtureExecutable(""); err != nil || got.executable != shim {
		t.Fatalf("ordinary executable: %q, %v", got, err)
	}
	if _, err := resolveBrowserFixtureExecutable(filepath.Join(directory, "missing")); err == nil {
		t.Fatal("missing override did not fail")
	}
}

// This opt-in fixture exercises browser-origin/authentication semantics using a
// real browser and the actual connector/socket/server. It starts no Codex/model.
func TestControlBrowser(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_CONTROL_BROWSER") != "1" {
		t.Skip("set MEKUGI_TEST_CONTROL_BROWSER=1 with agent-browser installed")
	}
	t.Setenv("AGENT_BROWSER_ENGINE", "chrome")
	browser := browserFixtureCommand(t)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	directory, err := os.MkdirTemp("", "mekugi-browser-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	listener, err := sessionapi.ListenControl(filepath.Join(directory, "control.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	launcher := &nativeReattachLauncher{prepare: prepareAPIInput, run: func(ctx context.Context, start sessionapi.Start, api *sessionapi.Server) error {
		if err := api.Publish(sessionapi.Frame{Session: sessionapi.Session{Thread: start.Resume, Workspace: directory}, Input: &sessionapi.Input{}}); err != nil {
			return err
		}
		if err := api.SetSummary(sessionapi.Summary{Thread: start.Resume, Workspace: directory, Ready: true}); err != nil {
			return err
		}
		for {
			select {
			case intent := <-api.Intents():
				intent.Reply <- ""
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}}
	hub := sessionapi.NewHub(ctx, launcher)
	defer hub.Close()
	server := &http.Server{Handler: sessionapi.Versioned(hub.Handler())}
	defer server.Close()
	go server.Serve(listener)
	transport := sessionapi.ControlTransport(filepath.Join(directory, "control.sock"))
	defer transport.CloseIdleConnections()
	rpc, err := sessionapi.OpenRPC(ctx, &http.Client{Transport: transport}, "http://mekugi"+sessionapi.RPCPath, "")
	if err != nil {
		t.Fatal(err)
	}
	defer rpc.Close()
	serverRPCCall(t, rpc, "session/start", "", sessionapi.Start{Workspace: directory, Model: "fixture", Resume: "thread"})
	var bridge *connector.Connector
	var remoteURL string
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rotate" && r.Method == http.MethodPost {
			if err := bridge.Rotate(); err != nil {
				http.Error(w, "rotation failed", 500)
			}
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, controlBrowserPage, remoteURL, sessionapi.RPCSubprotocol, sessionapi.ProtocolVersion)
	}))
	defer page.Close()
	// A fixed credential is confined to this disposable fixture and its page.
	tokenFile := filepath.Join(directory, "token")
	if err := os.WriteFile(tokenFile, []byte(strings.Repeat("a", 64)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bridge, err = connector.New(connector.Options{SocketPath: filepath.Join(directory, "control.sock"), TokenFile: tokenFile, Origins: []string{page.URL}})
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	remote := httptest.NewServer(bridge)
	defer remote.Close()
	remoteURL = strings.Replace(remote.URL, "http://", "ws://", 1) + sessionapi.RPCPath
	command := exec.CommandContext(ctx, browser, "session", "id", "--scope", "worktree", "--prefix", "control-rpc")
	session, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("browser session identity: %v: %s", err, session)
	}
	name := strings.TrimSpace(string(session))
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = exec.CommandContext(cleanup, browser, "--session", name, "close").Run()
	}()
	command = exec.CommandContext(ctx, browser, "--session", name, "--engine", "chrome", "open", page.URL)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("open browser: %v: %s", err, output)
	}
	command = exec.CommandContext(ctx, browser, "--session", name, "eval", "--stdin")
	command.Stdin = strings.NewReader(`(async () => { await window.controlResult; return "CONTROL_BROWSER_PASS"; })()`)
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "CONTROL_BROWSER_PASS") {
		t.Fatalf("browser protocol acceptance: %v: %s", err, output)
	}
}

const controlBrowserPage = `<!doctype html><title>Control protocol fixture</title><p>Browser control acceptance</p><script>
window.controlResult=(async()=>{
 const endpoint=%q, subprotocol=%q, version=%q, token="a".repeat(64);
 const check=(value,message)=>{if(!value)throw Error(message)};
 async function connect(){
  const ws=new WebSocket(endpoint,subprotocol), pending=new Map(), events=[];
  let next=0, eventWake;
  const closed=new Promise(resolve=>ws.addEventListener("close",()=>{resolve();for(const p of pending.values())p.reject(Error("closed"));pending.clear()}));
  ws.addEventListener("message",e=>{const m=JSON.parse(e.data);if(m.id!==undefined){const p=pending.get(m.id);if(p){pending.delete(m.id);p.resolve(m)}}else{events.push(m);if(eventWake){eventWake();eventWake=null}}});
  await new Promise((resolve,reject)=>{ws.onopen=resolve;ws.onerror=()=>reject(Error("upgrade failed"))});
  const call=(method,params)=>new Promise((resolve,reject)=>{const id=String(++next);pending.set(id,{resolve,reject});ws.send(JSON.stringify({jsonrpc:"2.0",id,method,params}))});
  const event=async()=>{while(!events.length)await new Promise(resolve=>eventWake=resolve);return events.shift()};
  const initialized=await call("initialize",{version,token});
  check(initialized.result?.version===version,"initialization failed");
  return {ws,call,event,closed};
 }
 const first=await connect();
 check((await first.call("session/list",{})).result.sessions[0].id==="thread","session list failed");
 check((await first.call("session/subscribe",{thread:"thread",subscription:"one"})).result.subscription==="one","subscription acknowledgement failed");
 const snapshot=await first.event();check(snapshot.method==="subscription/event"&&snapshot.params.kind==="snapshot","initial snapshot failed");
 check(!(await first.call("session/input",{thread:"thread",body:{text:"browser input"}})).error,"input failed");
 check((await first.call("native/start",{subscription:"forbidden",body:{workspace:"/"}})).error.data.status===403,"remote native authority was admitted");
 first.ws.close();await first.closed;
 const second=await connect();
 await second.call("session/subscribe",{thread:"thread",subscription:"two"});
 check((await second.event()).params.kind==="snapshot","reconnection snapshot failed");
 await fetch("/rotate",{method:"POST"});await second.closed;
 let refused=false;try{await connect()}catch{refused=true}
 check(refused,"old credential survived rotation");
 return true;
})();
window.controlResult=Promise.race([window.controlResult,new Promise((_,reject)=>setTimeout(()=>reject(Error("browser control timeout")),20000))]);
</script>`
