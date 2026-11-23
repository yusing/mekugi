package launch

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/backends/codex/binding"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
)

func TestAutoWrapProcess(t *testing.T) {
	if os.Getenv("MEKUGI_AUTO_WRAP_PROCESS") != "1" {
		return
	}
	if os.Args[3] == "router" {
		var args []string
		if os.Getenv("MEKUGI_AUTO_WRAP_TERMINAL") == "1" {
			args = []string{"--yolo"}
		}
		if os.Getenv("MEKUGI_AUTO_WRAP_HEADLESS") == "1" {
			args = []string{"headless", "--yolo"}
			if trust := os.Getenv("MEKUGI_AUTO_WRAP_TRUST"); trust != "" {
				args = append(args, "-c", "bypass_hook_trust="+trust)
			}
		}
		var routerArgs []string
		// The wrapper borrows this fixture's server. Its lifetime is joined here,
		// while detached process startup has separate subprocess acceptance.
		var stopServer context.CancelFunc
		var serverDone chan int
		if os.Getenv("MEKUGI_AUTO_WRAP_TERMINAL") == "1" {
			socket, err := sessionapi.ControlSocketPath()
			if err != nil {
				t.Fatal(err)
			}
			var serverContext context.Context
			serverContext, stopServer = context.WithCancel(t.Context())
			serverDone = make(chan int, 1)
			go func() { serverDone <- RunServer(serverContext, []string{"--socket", socket}, os.Stderr) }()
			if err := sessionapi.EnsureControl(t.Context(), socket, func() error {
				serverCLIAwait(t, func() bool { _, err := os.Stat(socket); return err == nil })
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}
		if os.Getenv("MEKUGI_AUTO_WRAP_HOOK_CHOICE") != "" {
			// Earlier startup output must not acknowledge a later hook decision.
			fmt.Fprintln(os.Stdout, "Ready")
		}
		code, err := wrapCodex(t.Context(), routerArgs, args)
		if stopServer != nil {
			stopServer()
			if serverCode := <-serverDone; serverCode != 0 {
				t.Fatalf("server exit: %d", serverCode)
			}
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(code)
	}
	if len(os.Args) > 3 && os.Args[3] == "codex" {
		scenario := os.Getenv("MEKUGI_AUTO_WRAP_HEADLESS_SCENARIO")
		headlessHost := os.Getenv("MEKUGI_AUTO_WRAP_HEADLESS") == "1"
		runtimeStarted := make(chan struct{})
		markerPath := os.Getenv("MEKUGI_AUTO_WRAP_RPC_MARKER")
		if argsMarker := os.Getenv("MEKUGI_AUTO_WRAP_ARGS_MARKER"); argsMarker != "" {
			if err := os.WriteFile(argsMarker, []byte(strings.Join(os.Args[4:], "\n")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		marker, err := os.OpenFile(markerPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			t.Fatal(err)
		}
		defer marker.Close()
		if headlessHost {
			interrupt := make(chan os.Signal, 1)
			signal.Notify(interrupt, os.Interrupt)
			go func() {
				<-interrupt
				_ = os.WriteFile(markerPath+".joined", []byte("joined"), 0600)
				os.Exit(0)
			}()
		}
		workStatus := ""
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			var request struct {
				ID     jsontext.Value `json:"id"`
				Method string         `json:"method"`
				Params struct {
					Cwds   []string       `json:"cwds"`
					Thread string         `json:"threadId"`
					Config map[string]any `json:"config"`
					Input  []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"input"`
				} `json:"params"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintln(marker, request.Method)
			switch request.Method {
			case "initialize":
				fmt.Fprintf(os.Stdout, `{"id":%s,"result":{}}`+"\n", request.ID)
			case "config/read":
				fmt.Fprintf(os.Stdout, `{"id":%s,"result":{"config":{"compact_prompt":"Fixture caller handover."},"layers":[{"name":{"type":"sessionFlags"},"config":{"hooks":{"PreToolUse":[],"state":{}}}}]}}`+"\n", request.ID)
			case "hooks/list":
				if request.Params.Thread == "auto-wrap" {
					request.Params.Cwds = []string{os.Getenv("MEKUGI_AUTO_WRAP_WORKSPACE")}
				}
				if len(request.Params.Cwds) != 1 {
					t.Fatal("hook discovery did not select one absolute workspace")
				}
				var hooks []map[string]any
				if os.Getenv("MEKUGI_AUTO_WRAP_HOOK_CHOICE") != "" {
					hooks = append(hooks, map[string]any{"key": "caller-modified", "eventName": "sessionStart", "enabled": true, "trustStatus": "modified", "handlerType": "command", "command": "bash scripts/startup.sh", "sourcePath": request.Params.Cwds[0] + "/.codex/hooks.json", "currentHash": "sha256:modified"})
				}
				result, err := json.Marshal(map[string]any{"data": []any{map[string]any{"cwd": request.Params.Cwds[0], "errors": []any{}, "hooks": hooks}}})
				if err != nil {
					t.Fatal(err)
				}
				fmt.Fprintf(os.Stdout, `{"id":%s,"result":%s}`+"\n", request.ID, result)
			case "config/batchWrite":
				fmt.Fprintf(os.Stdout, `{"id":%s,"result":{}}`+"\n", request.ID)
			case "model/list":
				fmt.Fprintf(os.Stdout, `{"id":%s,"result":{"data":[]}}`+"\n", request.ID)
			case "thread/subtree/list":
				agent := binding.ThreadInfo{ID: "auto-wrap", Source: []byte(`{"subAgent":{"thread_spawn":{"agent_path":"/root"}}}`), CanAcceptDirectInput: new(true)}
				if workStatus != "" {
					agent.AgentStatus, _ = json.Marshal(workStatus)
				}
				response, err := json.Marshal(map[string]any{"id": request.ID, "result": map[string]any{"data": []binding.ThreadInfo{agent}}})
				if err != nil {
					t.Fatal(err)
				}
				fmt.Fprintln(os.Stdout, string(response))
			case "environment/info":
				close(runtimeStarted)
				if headlessHost && scenario == "startup" {
					fmt.Fprintln(os.Stderr, "fixture: invalid model configuration")
					os.Exit(2)
				}
				fmt.Fprintf(os.Stdout, `{"id":%s,"result":{"shell":{"name":"bash","path":"/bin/bash"}}}`+"\n", request.ID)
			case "thread/start":
				if trust := os.Getenv("MEKUGI_AUTO_WRAP_TRUST"); trust != "" && request.Params.Config["bypass_hook_trust"] != (trust == "true") {
					t.Fatal("thread/start omitted explicit hook trust")
				}
				for _, arg := range os.Args {
					if strings.HasPrefix(arg, "experimental_compact_prompt_file=") || strings.HasPrefix(arg, "compact_prompt=") {
						t.Fatalf("launch injected a compaction prompt: %s", arg)
					}
				}
				fmt.Fprintf(os.Stdout, `{"id":%s,"result":{"thread":{"id":"auto-wrap","cwd":%q},"model":"test-model"}}`+"\n", request.ID, os.Getenv("MEKUGI_AUTO_WRAP_WORKSPACE"))
				if os.Getenv("MEKUGI_AUTO_WRAP_FAILURE") == "1" {
					time.Sleep(100 * time.Millisecond)
					os.Exit(23)
				}
			case "turn/start":
				switch scenario {
				case "rpc-rejected":
					fmt.Fprintf(os.Stdout, `{"id":%s,"error":{"code":-1,"message":"fixture: start rejected"}}`+"\n", request.ID)
					continue
				case "rpc-stale":
					fmt.Fprintf(os.Stdout, `{"id":%q,"error":{"code":-1,"message":"fixture: stale rejection"}}`+"\n", string(request.ID))
				}
				switch scenario {
				case "approval":
					fmt.Fprintln(os.Stdout, `{"id":"approval","method":"item/commandExecution/requestApproval","params":{"threadId":"auto-wrap"}}`)
					continue
				case "question":
					fmt.Fprintln(os.Stdout, `{"method":"item/completed","params":{"threadId":"auto-wrap","item":{"id":"question","type":"agentMessage","delivery":"async","questions":[{"id":"q"}]}}}`)
					continue
				case "disconnect":
					fmt.Fprintln(os.Stderr, "fixture: host transport ended")
					os.Exit(23)
				}
				if prompt := os.Getenv("MEKUGI_AUTO_WRAP_PROMPT"); prompt != "" && (len(request.Params.Input) != 1 || request.Params.Input[0].Text != prompt) {
					t.Fatal("literal stdin prompt was changed")
				}
				fmt.Fprintf(os.Stdout, `{"id":%s,"result":{"turn":{"id":"test-turn"}}}`+"\n", request.ID)
				fmt.Fprintln(os.Stdout, `{"method":"turn/started","params":{"threadId":"auto-wrap","turn":{"id":"test-turn"}}}`)
				status := "completed"
				if scenario == "failed" || scenario == "interrupted" {
					status = scenario
				}
				fmt.Fprintf(os.Stdout, `{"method":"turn/completed","params":{"threadId":"auto-wrap","turn":{"id":"test-turn","status":%q}}}`+"\n", status)
				workStatus = status
				if status == "failed" {
					workStatus = "errored"
				}
			}
		}
		if err := scanner.Err(); err != nil {
			t.Fatal(err)
		}
		if headlessHost {
			select {
			case <-runtimeStarted:
				if err := os.WriteFile(markerPath+".joined", []byte("joined"), 0600); err != nil {
					t.Fatal(err)
				}
			default:
			}
		}
		os.Exit(0) // Keep the test runner's PASS line out of the RPC stream.
	}

}

func TestWrapIntegratedUIAndRedirectedBehavior(t *testing.T) {
	for _, test := range []struct {
		name       string
		terminal   bool
		optOut     bool
		failure    bool
		hookChoice string
	}{
		{name: "terminal", terminal: true},
		{name: "terminal-hook-decline", terminal: true, hookChoice: "1"},
		{name: "terminal-hook-trust", terminal: true, hookChoice: "2"},
		{name: "terminal-env-zero", terminal: true, optOut: true},
		{name: "redirected"},
		{name: "terminal-failure", terminal: true, failure: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			cache := shortTempDir(t)
			t.Setenv("HOME", cache) // os.UserCacheDir uses HOME on macOS.
			t.Setenv("XDG_CACHE_HOME", cache)
			launcher := os.Args[0]
			workspace := t.TempDir()
			rpcMarker := filepath.Join(dir, "app-server-rpc")
			t.Setenv("MEKUGI_AUTO_WRAP_PROCESS", "1")
			t.Setenv("MEKUGI_AUTO_WRAP_HOOK_CHOICE", test.hookChoice)
			t.Setenv("MEKUGI_AUTO_WRAP_TERMINAL", "0")
			t.Setenv("MEKUGI_AUTO_WRAP_FAILURE", "0")
			if test.failure {
				t.Setenv("MEKUGI_AUTO_WRAP_FAILURE", "1")
			}
			if test.terminal {
				t.Setenv("MEKUGI_AUTO_WRAP_TERMINAL", "1")
			}
			t.Setenv("MEKUGI_AUTO_WRAP_RPC_MARKER", rpcMarker)
			t.Setenv("MEKUGI_APP_SERVER_UI", "")
			if test.optOut {
				t.Setenv("MEKUGI_APP_SERVER_UI", "0")
			} else if err := os.Unsetenv("MEKUGI_APP_SERVER_UI"); err != nil {
				t.Fatal(err)
			}
			t.Setenv("MEKUGI_AUTO_WRAP_WORKSPACE", workspace)
			t.Setenv("CODEX_HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			t.Setenv("MEKUGI_RUNTIME_DIR", t.TempDir())
			// Keep installed runtime tools such as Node.js available.
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			for name, script := range map[string]string{
				"mekugi-app-server": "#!/bin/sh\nexec '" + strings.ReplaceAll(os.Args[0], "'", "'\\''") + "' -test.run=^TestAutoWrapProcess$ -- codex \"$@\"\n",
			} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, launcher, "-test.run=^TestAutoWrapProcess$", "--", "router")
			var output []byte
			var err error
			var sent []string
			if test.terminal {
				tty, startErr := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 100})
				if startErr != nil {
					t.Fatal(startErr)
				}
				defer tty.Close()
				read := make(chan []byte, 1)
				go func() {
					var data bytes.Buffer
					screen := vt.NewEmulator(100, 24)
					stopReplies := startEmulatorReplies(io.Discard, screen)
					buf := make([]byte, 8192)
					quit, answeredHook := false, false
					for {
						n, readErr := tty.Read(buf)
						data.Write(buf[:n])
						_, _ = screen.Write(buf[:n])
						visible := screen.String()
						promptOpen := strings.Contains(visible, "Trust this hook?")
						if test.hookChoice != "" && !answeredHook && promptOpen {
							sent = append(sent, test.hookChoice+"\r")
							_, _ = tty.Write([]byte(test.hookChoice + "\r"))
							answeredHook = true
						} else if !test.failure && !quit && (test.hookChoice == "" || answeredHook) && !promptOpen && composerStatusShows(visible, "Ready") {
							sent = append(sent, "/quit\r")
							_, _ = tty.Write([]byte("/quit\r"))
							quit = true
						}
						if readErr != nil {
							break
						}
					}
					stopReplies()
					read <- data.Bytes()
				}()
				err = cmd.Wait()
				output = <-read
			} else {
				output, err = cmd.CombinedOutput()
			}
			if test.failure {
				if err == nil || cmd.ProcessState.ExitCode() != 23 || !bytes.Contains(output, []byte("app-server disconnected; active work may be incomplete")) {
					t.Fatalf("unexpected app-server exit lost its status or diagnostic: %v\n%s", err, output)
				}
			} else if !test.terminal {
				if err == nil || cmd.ProcessState.ExitCode() != 2 || !bytes.Contains(output, []byte("interactive sessions require terminal stdin and stdout")) {
					t.Fatalf("redirected ordinary invocation was not rejected: %v\n%s", err, output)
				}
			} else if err != nil {
				if test.hookChoice != "" {
					screen := vt.NewEmulator(100, 24)
					stopReplies := startEmulatorReplies(io.Discard, screen)
					defer stopReplies()
					_, _ = screen.Write(output)
					t.Fatalf("wrapper: %v; sent inputs: %q\n%s", err, sent, screen.String())
				}
				t.Fatalf("wrapper: %v\n%s", err, output)
			}
			if test.terminal {
				// The second stat line leads with the painted "Main" label.
				if !bytes.Contains(output, []byte("\x1b[?1049h")) || !bytes.Contains(output, []byte("mMain\x1b")) {
					t.Fatalf("terminal wrapper did not render native Main: %q", output)
				}
				calls, readErr := os.ReadFile(rpcMarker)
				handshake := "initialize\ninitialized\nenvironment/info\nthread/start\n"
				if readErr != nil || !bytes.Contains(calls, []byte(handshake)) {
					t.Fatalf("native app-server handshake: %q, %v", calls, readErr)
				}
				if !test.failure && !bytes.Contains(calls, []byte("hooks/list\n")) {
					t.Fatalf("hook inventory not requested: %s", calls)
				}
				if test.hookChoice != "" && bytes.Contains(calls, []byte("config/batchWrite\n")) != (test.hookChoice == "2") {
					t.Fatalf("hook trust decision: %s", calls)
				}
				if test.hookChoice != "" && (!bytes.Contains(output, []byte("Trust: modified")) || !bytes.Contains(output, []byte("bash scripts/startup.sh"))) {
					t.Fatal("rendered startup hook review omitted changed trust status or command")
				}
			} else if bytes.Contains(output, []byte("\x1b[?1049h")) || bytes.Contains(output, []byte("Main • ")) {
				t.Fatalf("redirected wrapper unexpectedly rendered the integrated UI: %q", output)
			} else if _, statErr := os.Stat(rpcMarker); !os.IsNotExist(statErr) {
				t.Fatalf("redirected wrapper unexpectedly started app-server: %v", statErr)
			}
			if !test.terminal {
				if bytes.ContainsAny(output, "\x1b\x07") {
					t.Fatalf("launcher emitted provider terminal controls: %q", output)
				}
			}
		})
	}
}

func TestInteractiveCodexArgs(t *testing.T) {
	for _, test := range []struct {
		args []string
		want bool
	}{
		{nil, true}, {[]string{"resume", "--last"}, true}, {[]string{"--model", "exec", "hello"}, true},
		{[]string{"exec", "hello"}, false}, {[]string{"--model", "gpt-6", "exec", "hello"}, false},
		{[]string{"--help"}, false}, {[]string{"--version"}, false}, {[]string{"-c", "help", "prompt"}, true},
	} {
		if got := interactiveCodexArgs(test.args); got != test.want {
			t.Errorf("%v: %v, want %v", test.args, got, test.want)
		}
	}
}
