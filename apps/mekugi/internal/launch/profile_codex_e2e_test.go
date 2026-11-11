//go:build codex_e2e

package launch

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/yusing/mekugi/apps/mekugi/internal/headless"
	launchoptions "github.com/yusing/mekugi/apps/mekugi/internal/launch/options"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestProfileLaunchInstalledCodex(t *testing.T) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	for _, auth := range []string{"anonymous", "env", "literal", "managed", "command", "websocket", "refresh", "remote", "unknown"} {
		t.Run(auth, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
			defer cancel()
			environment := routerFaultCodexEnvironment(t)
			home := launchoptions.EnvironmentValue(environment, "CODEX_HOME")
			workspace := t.TempDir()
			if auth == "anonymous" {
				if err := os.WriteFile(filepath.Join(workspace, "preload.txt"), []byte("HTTP_CHILD_PRELOAD_CONTENT"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			catalog, err := bundledModelCatalog(ctx, codex, workspace)
			if err != nil {
				t.Fatal(err)
			}
			var parsed map[string]any
			if err := json.Unmarshal(catalog, &parsed); err != nil {
				t.Fatal(err)
			}
			model := parsed["models"].([]any)[0].(map[string]any)
			model["tool_mode"] = "code_mode"
			model["multi_agent_reasoning_effort"] = "high"
			parsed["models"] = []any{model}
			catalog, err = json.Marshal(&parsed)
			if err != nil {
				t.Fatal(err)
			}
			catalogPath := filepath.Join(workspace, "source catalog.json")
			if err := os.WriteFile(catalogPath, catalog, 0600); err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			steps := map[string]int{}
			parent := ""
			refreshed := false
			catalogRequests := 0
			searchRequests := 0
			socketRequests, continuations := 0, 0
			expectedEffort := "high"
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if auth == "remote" && r.URL.Path == "/catalog" {
					mu.Lock()
					catalogRequests++
					mu.Unlock()
					if r.Header.Get("Authorization") != "Bearer fixture-profile-token" || !strings.Contains(r.URL.RawQuery, "catalog=a%2Bb") {
						t.Error("remote catalog authentication/query changed")
					}
					w.Header().Set("Content-Type", "application/json")
					w.Write(catalog)
					return
				}
				search := r.URL.Path == "/tenant/v1/alpha/search"
				if r.URL.Path != "/tenant/v1/responses" && !search || r.URL.RawQuery != "version=a%2Bb" || r.Header.Get("X-Provider") != "native" || r.Header.Get("Chatgpt-Account-Id") != "" {
					t.Error("profile endpoint/query/header or account isolation failed")
				}
				if (auth == "env" || auth == "websocket") && r.Header.Get("X-Inherited") != "admitted-header" {
					t.Error("environment-backed inherited provider header lost")
				}
				wantAuth := "Bearer fixture-profile-token"
				if auth == "anonymous" || auth == "websocket" || auth == "unknown" {
					wantAuth = ""
				}
				if auth == "refresh" && r.Header.Get("Authorization") == "Bearer stale-profile-token" {
					mu.Lock()
					refreshed = true
					mu.Unlock()
					w.WriteHeader(401)
					io.WriteString(w, `{"error":{"type":"authentication_error","message":"fixture token expired"}}`)
					return
				}
				if r.Header.Get("Authorization") != wantAuth {
					t.Error("native authentication did not reach pinned provider")
				}
				if search {
					mu.Lock()
					searchRequests++
					mu.Unlock()
					w.Header().Set("Content-Type", "application/json")
					io.WriteString(w, `{"output":"PROFILE_SEARCH_RESULT"}`)
					return
				}
				deliver := func(w http.ResponseWriter, body []byte, requestHeaders http.Header) {
					var wire map[string]any
					if json.Unmarshal(body, &wire) != nil {
						t.Error("invalid profile request")
						http.Error(w, "invalid", 500)
						return
					}
					if wire["generate"] == false {
						profileFixtureReply(w, "prewarm", nil)
						return
					}
					if wire["model"] == sessionTitleModel {
						if requestHeaders.Get(codexresponses.TurnMetadataHeader) != "" || requestHeaders.Get(codexresponses.ThreadIDHeader) != "" || requestHeaders.Get("x-codex-turn-state") != "" {
							t.Error("profile naming leaked execution identity")
						}
						profileFixtureReply(w, "title", map[string]any{"type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]string{"type": "output_text", "text": "Profile fixture task"}}})
						return
					}
					var native struct {
						Input []map[string]jsontext.Value `json:"input"`
					}
					if err := json.Unmarshal(body, &native); err != nil {
						t.Error(err)
						http.Error(w, "invalid input", http.StatusBadRequest)
						return
					}
					for _, item := range native.Input {
						if codexresponses.StandaloneFunctionOutput(item) {
							t.Error("HTTP/profile provider received a native output without call_id")
							http.Error(w, "function_call_output requires call_id", http.StatusBadRequest)
							return
						}
					}
					if wire["access_programs"] != nil {
						t.Errorf("model/policy changed: %v", wire["model"])
					}
					mu.Lock()
					wantEffort := expectedEffort
					if wire["previous_response_id"] != nil {
						continuations++
					}
					mu.Unlock()
					if reasoning, ok := wire["reasoning"].(map[string]any); !ok || reasoning["effort"] != wantEffort {
						t.Error("profile reasoning effort changed")
					}
					metadata, ok := codexresponses.DecodeTurnMetadata(requestHeaders)
					if !ok || metadata.ThreadID == "" {
						t.Error("native metadata lost")
						http.Error(w, "metadata", 500)
						return
					}
					mu.Lock()
					if parent == "" {
						parent = metadata.ThreadID
					}
					isParent := metadata.ThreadID == parent
					steps[metadata.ThreadID]++
					step := steps[metadata.ThreadID]
					mu.Unlock()
					program, answer := "", ""
					if isParent {
						switch step {
						case 1:
							program = `text(await tools.mcp__threadcontrol__spawn_agent({task_name:"worker",message:"Run one stock JavaScript shell tool, then finish",fork_turns:"none"}));`
							if auth == "anonymous" {
								program = strings.Replace(program, `fork_turns:"none"`, `fork_turns:"none",preload:{sources:["@preload.txt"]}`, 1)
							}
						case 2:
							program = `text(await tools.mcp__threadcontrol__wait_agent({wait_mode:"all"}));`
						default:
							answer = "PROFILE_PARENT_COMPLETE"
						}
					} else if step == 1 {
						if auth == "anonymous" && !bytes.Contains(body, []byte("HTTP_CHILD_PRELOAD_CONTENT")) {
							t.Error("fresh HTTP child did not receive its preload before inference")
						}
						program = `text(await tools.exec_command({cmd:"printf PROFILE_TOOL_ONCE >> profile-effect.txt",shell:"/bin/bash",login:false}));`
						if auth == "literal" {
							program += `text(await tools.web__run({search_query:[{q:"profile fixture"}]}));`
						}
					} else {
						if auth == "literal" && !bytes.Contains(body, []byte("PROFILE_SEARCH_RESULT")) {
							t.Error("native search result missing from child continuation")
						}
						answer = "PROFILE_CHILD_COMPLETE"
					}
					id := fmt.Sprintf("profile-%s-%d", metadata.ThreadID, step)
					item := map[string]any{"type": "message", "id": id, "role": "assistant", "phase": "final_answer", "status": "completed", "content": []any{map[string]string{"type": "output_text", "text": answer}}}
					if program != "" {
						item = map[string]any{"type": "custom_tool_call", "id": id, "call_id": id + "-call", "name": "exec", "namespace": "functions", "input": program, "status": "completed"}
					}
					profileFixtureReply(w, id, item)
				}
				if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
					mu.Lock()
					socketRequests++
					mu.Unlock()
					conn, err := websocket.Accept(w, r, nil)
					if err != nil {
						t.Error(err)
						return
					}
					defer conn.CloseNow()
					conn.SetReadLimit(codexresponses.MaxRequestBytes)
					for {
						_, body, err := conn.Read(r.Context())
						if err != nil {
							return
						}
						var wire struct {
							Metadata map[string]string `json:"client_metadata"`
						}
						_ = json.Unmarshal(body, &wire)
						headers := r.Header.Clone()
						for key, value := range wire.Metadata {
							headers.Set(key, value)
						}
						recorder := httptest.NewRecorder()
						deliver(recorder, body, headers)
						for line := range strings.SplitSeq(recorder.Body.String(), "\n") {
							if payload, ok := strings.CutPrefix(line, "data: "); ok {
								if err := conn.Write(r.Context(), websocket.MessageText, []byte(payload)); err != nil {
									return
								}
							}
						}
					}
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					return
				}
				deliver(w, body, r.Header)
			}))
			defer upstream.Close()
			authConfig := ""
			switch auth {
			case "literal":
				authConfig = "experimental_bearer_token=\"fixture-profile-token\"\n"
			case "managed":
				if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"OPENAI_API_KEY":"fixture-profile-token"}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "remote":
				authConfig = "env_key=\"PROFILE_FIXTURE_TOKEN\"\nmodel_catalog_url=" + strconv.Quote(upstream.URL+"/catalog?catalog=a%2Bb") + "\n"
				environment = append(environment, "PROFILE_FIXTURE_TOKEN=fixture-profile-token")
			case "env":
				authConfig = "env_key=\"PROFILE_FIXTURE_TOKEN\"\n"
				environment = append(environment, "PROFILE_FIXTURE_TOKEN=fixture-profile-token")
			case "command", "refresh":
				command := filepath.Join(workspace, "token-helper")
				if err := os.WriteFile(command, []byte("#!/bin/sh\n[ \"$1\" = '--literal' ] && [ \"$2\" = 'space value' ] && [ \"$PROFILE_COMMAND_DEP\" = 'admitted' ] || exit 9\nprintf '%s' 'fixture-profile-token'\n"), 0700); err != nil {
					t.Fatal(err)
				}
				authConfig = `auth={command=` + strconv.Quote(command) + `,args=["--literal","space value"],timeout_ms=5000}` + "\n"
				environment = append(environment, "PROFILE_COMMAND_DEP=admitted")
				if auth == "refresh" {
					marker := filepath.Join(workspace, "auth-called")
					if err := os.WriteFile(command, []byte("#!/bin/sh\nif [ -f "+quoteShellWord(marker)+" ]; then printf fixture-profile-token; else touch "+quoteShellWord(marker)+"; printf stale-profile-token; fi\n"), 0700); err != nil {
						t.Fatal(err)
					}
				}
			}
			profile := fmt.Sprintf("model_provider=\"custom\"\nmodel_reasoning_effort=\"high\"\nmodel_context_window=250000\nmodel_auto_compact_token_limit=240000\nmodel_catalog_json=%q\n[model_providers.custom]\nname=\"Native fixture\"\nbase_url=%q\nwire_api=\"responses\"\nrequires_openai_auth=false\nsupports_websockets=false\nquery_params={version=\"a%%2Bb\"}\nhttp_headers={\"X-Provider\"=\"native\"}\n%s", catalogPath, upstream.URL+"/tenant/v1", authConfig)
			if auth == "literal" {
				// Codex also enables search by provider identity, without the
				// supports_standalone_web_search flag. The gateway must not veto it.
				profile = "web_search=\"live\"\n" + strings.Replace(profile, "name=\"Native fixture\"", "name=\"OpenAI\"", 1)
			}
			if auth == "websocket" {
				profile = strings.Replace(profile, "supports_websockets=false", "supports_websockets=true", 1)
			}
			if auth == "managed" {
				profile = strings.Replace(profile, "requires_openai_auth=false", "requires_openai_auth=true", 1)
			}
			if auth == "remote" || auth == "unknown" {
				profile = strings.Replace(profile, fmt.Sprintf("model_catalog_json=%q\n", catalogPath), "", 1)
			}
			// Provider capabilities and native headers may come from the ordinary
			// user layer while only the provider ID and endpoint are imported.
			if auth == "websocket" || auth == "env" {
				base := "[model_providers.custom]\nsupports_websockets=" + strconv.FormatBool(auth == "websocket") + "\nstream_idle_timeout_ms=900000\nwebsocket_connect_timeout_ms=20000\nenv_http_headers={\"X-Inherited\"=\"PROFILE_HEADER\"}\n"
				if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(base), 0600); err != nil {
					t.Fatal(err)
				}
				profile = strings.Replace(profile, "supports_websockets="+strconv.FormatBool(auth == "websocket")+"\n", "", 1)
				environment = append(environment, "PROFILE_HEADER=admitted-header")
			}
			if auth == "env" {
				environment = append(environment, "MEKUGI_PROFILE_ADMITTED_ENV=1")
			}
			if err := os.WriteFile(filepath.Join(home, "work.config.toml"), []byte(profile), 0600); err != nil {
				t.Fatal(err)
			}
			environment = append(environment, "MEKUGI_PROFILE_PROCESS=1", routerTestWorkerEnvironment+"=1", "MEKUGI_PROFILE_CAPTURE="+filepath.Join(workspace, "capture.jsonl"))
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProfileLaunchProcess$")
			cmd.Env, cmd.Dir, cmd.Stdin = environment, workspace, strings.NewReader("Delegate one fixture task and wait for its completion")
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("profile native launch failed: %v\nstdout: %.12000s\nstderr: %.12000s", err, &stdout, &stderr)
			}
			if !strings.Contains(stdout.String(), "PROFILE_PARENT_COMPLETE") || !strings.Contains(stdout.String(), "PROFILE_CHILD_COMPLETE") || !strings.Contains(stdout.String(), "mekugi/headless/completed") {
				t.Fatalf("family completion missing: %.12000s", &stdout)
			}
			effect, err := os.ReadFile(filepath.Join(workspace, "profile-effect.txt"))
			if err != nil || string(effect) != "PROFILE_TOOL_ONCE" {
				t.Fatalf("stock tool execution count: %q %v", effect, err)
			}
			captures, err := filepath.Glob(filepath.Join(workspace, "capture.jsonl.runtimes", "*", "capture.jsonl"))
			if err != nil || len(captures) != 1 {
				t.Fatalf("child captures: %q %v", captures, err)
			}
			captures = append(captures, filepath.Join(workspace, "capture.jsonl"))
			for _, capture := range captures {
				if info, err := os.Stat(capture); err != nil || info.Size() == 0 {
					t.Fatalf("normal capture missing: %v", err)
				}
			}
			if after, _ := os.ReadFile(catalogPath); !bytes.Equal(after, catalog) {
				t.Fatal("source catalog modified")
			}
			mu.Lock()
			if auth == "refresh" && !refreshed {
				t.Error("host authentication refresh was not exercised")
			}
			if auth == "remote" && catalogRequests == 0 {
				t.Error("native remote catalog was not exercised")
			}
			if auth == "literal" && searchRequests != 1 {
				t.Errorf("native search executions: %d", searchRequests)
			}
			if auth == "websocket" && (socketRequests == 0 || continuations == 0) {
				t.Errorf("native dedicated socket/ancestry not exercised: %d sockets, %d continuations", socketRequests, continuations)
			}
			if len(steps) != 2 {
				t.Errorf("runtime identities: %v", steps)
			}
			mu.Unlock()
			if auth == "anonymous" {
				// Stop every runtime, modify the selected import, then resume the
				// same Mekugi identity through ordinary native server admission.
				profile = strings.Replace(profile, "model_reasoning_effort=\"high\"", "model_reasoning_effort=\"low\"", 1)
				profile = strings.Replace(profile, "model_context_window=250000", "model_context_window=260000", 1)
				profile = strings.Replace(profile, "model_auto_compact_token_limit=240000", "model_auto_compact_token_limit=245000", 1)
				if err := os.WriteFile(filepath.Join(home, "work.config.toml"), []byte(profile), 0600); err != nil {
					t.Fatal(err)
				}
				mu.Lock()
				expectedEffort = "low"
				resume := parent
				mu.Unlock()
				resumed := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProfileLaunchProcess$")
				resumed.Env = append(environment, "MEKUGI_PROFILE_RESUME="+resume)
				resumed.Dir = workspace
				var output bytes.Buffer
				resumed.Stdout, resumed.Stderr = &output, &output
				if err := resumed.Run(); err != nil {
					t.Fatalf("fresh profile resume: %v\n%.12000s", err, &output)
				}
				if !strings.Contains(output.String(), "PROFILE_PARENT_COMPLETE") || !strings.Contains(output.String(), `"model_context_window":260000`) || !strings.Contains(output.String(), `"model_auto_compact_token_limit":245000`) {
					t.Fatalf("fresh resume lost imported settings/completion: %.12000s", &output)
				}
				mu.Lock()
				if parent != resume || len(steps) != 2 || steps[resume] != 4 {
					t.Errorf("fresh resume changed runtime identity or repeated work: %v", steps)
				}
				mu.Unlock()
			}
		})
	}
}

func profileFixtureReply(w http.ResponseWriter, id string, item map[string]any) {
	output := []any{}
	if item != nil {
		output = append(output, item)
	}
	events := []map[string]any{{"type": "response.created", "response": map[string]any{"id": id, "status": "in_progress", "output": []any{}}}}
	if item != nil {
		events = append(events, map[string]any{"type": "response.output_item.added", "output_index": 0, "item": item}, map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item})
	}
	events = append(events, map[string]any{"type": "response.completed", "response": map[string]any{"id": id, "status": "completed", "output": output}})
	w.Header().Set("Content-Type", "text/event-stream")
	for _, event := range events {
		body, _ := json.Marshal(&event)
		if event["type"] == "response.completed" {
			// The terminal may encode the same source differently from item.done.
			// The child's shell redirection exercises durable replay equivalence.
			body = bytes.ReplaceAll(body, []byte(">"), []byte(`\u003e`))
		}
		fmt.Fprintf(w, "data: %s\n\n", body)
	}
}

func TestProfileLaunchProcess(t *testing.T) {
	if os.Getenv("MEKUGI_PROFILE_PROCESS") != "1" {
		t.Skip("isolated profile launch")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Second)
	defer cancel()
	if resume := os.Getenv("MEKUGI_PROFILE_RESUME"); resume != "" || os.Getenv("MEKUGI_PROFILE_ADMITTED_ENV") == "1" {
		workspace, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		stream := headless.NewStream(os.Stdout, cancel)
		launcher := &serverLauncher{threads: control.New(), headless: stream}
		hub := sessionapi.NewHub(ctx, launcher)
		launcher.hub = hub
		args := []string{"-pwork", "--yolo", "-c", "features.plugins=false", "-c", "bypass_hook_trust=true"}
		if resume != "" {
			args = append(args, "resume", resume)
		}
		admitted := os.Environ()
		if os.Getenv("MEKUGI_PROFILE_ADMITTED_ENV") == "1" {
			// The already-running daemon has neither the caller's profile home,
			// executable PATH nor native auth dependencies.
			os.Setenv("CODEX_HOME", t.TempDir())
			os.Setenv("PATH", "/daemon-has-no-codex")
			os.Unsetenv("PROFILE_FIXTURE_TOKEN")
			os.Unsetenv("PROFILE_HEADER")
		}
		thread, err := hub.RunNative(ctx, sessionapi.NativeStart{Workspace: workspace, RouterArgs: []string{"--shell-guard=false", "--capture-output", os.Getenv("MEKUGI_PROFILE_CAPTURE")}, Args: args, Environment: admitted}, "Continue the fixture task")
		hub.Close()
		launcher.workers.Close()
		if err != nil {
			t.Fatal(err)
		}
		if err := stream.Complete(thread); err != nil {
			t.Fatal(err)
		}
		return
	}
	code, err := wrapCodex(ctx, []string{"--shell-guard=false", "--capture-output", os.Getenv("MEKUGI_PROFILE_CAPTURE")}, []string{"headless", "--yolo", "-p", "work", "-c", "features.plugins=false", "-c", "bypass_hook_trust=true"})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(code)
}
