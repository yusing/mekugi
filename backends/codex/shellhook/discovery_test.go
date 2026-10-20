package shellhook

import (
	"bufio"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A single JSON-lines subprocess models the preflight RPC boundary. Its journal
// records every request, including any accidental thread or turn creation.
func TestGuardDiscoveryChild(t *testing.T) {
	directory := os.Getenv("MEKUGI_DISCOVERY_FIXTURE")
	if directory == "" {
		return
	}
	mode := os.Getenv("MEKUGI_DISCOVERY_MODE")
	journal, err := os.Create(filepath.Join(directory, "journal"))
	if err != nil {
		os.Exit(2)
	}
	cwd, _ := os.Getwd()
	fmt.Fprintf(journal, "%s\n%d\n", cwd, os.Getpid())
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		fmt.Fprintf(journal, "%s\n", line)
		var request struct {
			ID     jsontext.Value `json:"id"`
			Method string         `json:"method"`
		}
		if json.Unmarshal(line, &request) != nil {
			os.Exit(3)
		}
		var result any = map[string]any{}
		switch request.Method {
		case "initialize":
		case "initialized":
			continue
		case "config/read":
			if mode == "cancel" {
				if os.WriteFile(filepath.Join(directory, "ready"), []byte("ready"), 0600) != nil {
					os.Exit(4)
				}
				for {
					time.Sleep(time.Hour)
				}
			}
			if mode == "disconnect" {
				os.Exit(0)
			}
			result = map[string]any{
				"config": map[string]any{"hooks": map[string]any{"PreToolUse": []any{map[string]any{"matcher": "merged-must-not-copy"}}}},
				"layers": []any{
					map[string]any{"name": map[string]any{"type": "user"}, "config": map[string]any{"hooks": map[string]any{"PreToolUse": []any{map[string]any{"matcher": "user-must-not-copy"}}}}},
					map[string]any{"name": map[string]any{"type": "sessionFlags"}, "config": map[string]any{"hooks": map[string]any{"PreToolUse": []any{map[string]any{"matcher": "Read", "hooks": []any{}}}, "state": map[string]any{"recovery": map[string]any{"enabled": true, "trusted_hash": "keep"}}}}},
				},
			}
			if mode == "no-layers" {
				result = map[string]any{"config": map[string]any{}}
			}
		case "hooks/list":
			if mode == "rpc-error" {
				encoded, _ := json.Marshal(map[string]any{"id": request.ID, "error": map[string]any{"code": -32000, "message": "fixture hook failure"}})
				fmt.Printf("%s\n", encoded)
				continue
			}
			hooks := []map[string]any{}
			for i, change := range []map[string]any{
				{}, {"matcher": "shell|Read"}, {"async": true}, {"matcher": "Read"}, {"eventName": "sessionStart"}, {"trustStatus": "untrusted"}, {"enabled": false},
			} {
				entry := guardHookTestEntry("caller")
				entry["key"] = fmt.Sprintf("caller:%d", i)
				for key, value := range change {
					entry[key] = value
				}
				if mode == "managed" && i == 0 {
					entry["isManaged"] = true
					entry["trustStatus"] = "managed"
				}
				if mode == "unverifiable-matcher" && i == 0 {
					entry["matcher"] = `^[A-Z&&[^A]]ash$`
				}
				hooks = append(hooks, entry)
			}
			result = map[string]any{"data": []any{map[string]any{"cwd": cwd, "errors": []any{}, "hooks": hooks}}}
		default:
			os.Exit(5)
		}
		encoded, err := json.Marshal(map[string]any{"id": request.ID, "result": result})
		if err != nil {
			os.Exit(6)
		}
		fmt.Printf("%s\n", encoded)
	}
	fmt.Fprintln(journal, "EOF")
	journal.Close()
	os.Exit(0)
}

func TestGuardDiscoveryInvocationAndLifecycle(t *testing.T) {
	for _, mode := range []string{"success", "managed", "unverifiable-matcher", "rpc-error", "no-layers", "disconnect", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			directory := t.TempDir()
			// Passing a relative selected workspace must still produce absolute RPC cwd.
			cwd, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			relative, err := filepath.Rel(cwd, directory)
			if err != nil {
				t.Fatal(err)
			}
			command := exec.Command(os.Args[0], "-test.run=^TestGuardDiscoveryChild$")
			command.Dir = relative
			command.Env = append(os.Environ(), "MEKUGI_DISCOVERY_FIXTURE="+directory, "MEKUGI_DISCOVERY_MODE="+mode)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if mode == "cancel" {
				go func() {
					ticker := time.NewTicker(time.Millisecond)
					defer ticker.Stop()
					for {
						select {
						case <-ctx.Done():
							return
						case <-ticker.C:
							if _, err := os.Stat(filepath.Join(directory, "ready")); err == nil {
								cancel()
								return
							}
						}
					}
				}()
			}
			prior, disabled, err := Discover(ctx, command)
			if mode == "success" {
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(disabled, []string{"caller:0", "caller:1"}) {
					t.Fatalf("disabled=%v", disabled)
				}
				var groups []map[string]any
				groupJSON, marshalErr := json.Marshal(prior.PreToolUse)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				if err := json.Unmarshal(groupJSON, &groups); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(groups, []map[string]any{{"matcher": "Read", "hooks": []any{}}}) {
					t.Fatalf("session groups=%s", groupJSON)
				}
				var state map[string]any
				if err := json.Unmarshal(prior.State["recovery"], &state); err != nil || state["trusted_hash"] != "keep" || state["enabled"] != true {
					t.Fatalf("lost session state: %s (%v)", prior.State["recovery"], err)
				}
			} else {
				if err == nil {
					t.Fatal("discovery accepted failed preflight")
				}
				expected := map[string]string{"managed": "managed shell hook", "unverifiable-matcher": "cannot verify hook matcher", "rpc-error": "fixture hook failure", "no-layers": "configuration layers", "disconnect": "disconnected"}
				if fragment := expected[mode]; fragment != "" && !strings.Contains(err.Error(), fragment) {
					t.Fatalf("wrong failure: %v", err)
				}
				if mode == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation error=%v", err)
				}
			}
			data, readErr := os.ReadFile(filepath.Join(directory, "journal"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(lines) < 4 || lines[0] != directory {
				t.Fatalf("invalid process journal: %s", data)
			}
			var pid int
			if _, err := fmt.Sscan(lines[1], &pid); err != nil {
				t.Fatal(err)
			}
			process, err := os.FindProcess(pid)
			if err != nil {
				t.Fatal(err)
			}
			if err := process.Signal(syscall.Signal(0)); !errors.Is(err, os.ErrProcessDone) && !errors.Is(err, syscall.ESRCH) {
				t.Fatalf("preflight child %d was not reaped: %v", pid, err)
			}
			process.Release()
			var methods []string
			for _, line := range lines[2:] {
				if line == "EOF" {
					continue
				}
				var request struct {
					Method string `json:"method"`
					Params struct {
						Cwd           string   `json:"cwd"`
						IncludeLayers bool     `json:"includeLayers"`
						Cwds          []string `json:"cwds"`
					} `json:"params"`
				}
				if err := json.Unmarshal([]byte(line), &request); err != nil {
					t.Fatal(err)
				}
				methods = append(methods, request.Method)
				if request.Method == "config/read" && (request.Params.Cwd != directory || !request.Params.IncludeLayers) {
					t.Fatalf("config request=%s", line)
				}
				if request.Method == "hooks/list" && !reflect.DeepEqual(request.Params.Cwds, []string{directory}) {
					t.Fatalf("hook request=%s", line)
				}
			}
			want := []string{"initialize", "initialized", "config/read"}
			if mode != "no-layers" && mode != "disconnect" && mode != "cancel" {
				want = append(want, "hooks/list")
			}
			if !reflect.DeepEqual(methods, want) {
				t.Fatalf("RPC methods=%v want=%v", methods, want)
			}
			if mode != "disconnect" && mode != "cancel" && lines[len(lines)-1] != "EOF" {
				t.Fatalf("preflight was not gracefully joined: %s", data)
			}
		})
	}
}
