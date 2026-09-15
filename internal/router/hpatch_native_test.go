package router

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// This opt-in bridge keeps the actual router's temporary retention alive while
// a real Code Mode host executes generated carriers. The client supplies native
// exec_command/write_stdin/apply_patch, including real hard cell termination.
// POST /translate receives HPATCH source; POST /close releases the fixture.
func TestHpatchNativeFixture(t *testing.T) {
	directory := os.Getenv("MEKUGI_HPATCH_NATIVE_FIXTURE")
	if directory == "" {
		t.Skip("set MEKUGI_HPATCH_NATIVE_FIXTURE to a session-created temporary directory")
	}
	transform, _ := mixedTestTransform(t)
	transform.proxy.translator = newInProcessMekugiTranslator(t.TempDir())
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	transform.proxy.replayStore = store
	if err := os.WriteFile(filepath.Join(transform.directory, "native-blocker"), []byte("regular file, not a directory\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(directory, "fixture-shell")
	threadID := strings.TrimPrefix(filepath.Base(transform.shellDirectory), "mekugi-scripts-")
	wrapperSource := "#!/bin/sh\nexport MEKUGI_HPATCH_WORKER_TEST=1\nexport MEKUGI_RUNTIME_DIR=" +
		shellQuoteArgument(filepath.Dir(transform.shellDirectory)) + "\nexport CODEX_THREAD_ID=" +
		shellQuoteArgument(threadID) + "\nif [ \"$#\" = 0 ]; then\nexec " +
		shellQuoteArgument(executable) + " -test.run='^TestHpatchMixedProcess$' --\nfi\ninterpreter=$1\nshift\nexec \"$interpreter\" -c \"$1\"\n"
	if err := os.WriteFile(wrapper, []byte(wrapperSource), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	count := 0
	recoveries := make(map[string]hpatchRecovery)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /translate", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		body, err := io.ReadAll(io.LimitReader(r.Body, maxMekugiScriptBytes+1))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		count++
		callID := fmt.Sprintf("native-%d", count)
		var history mekugiHistory
		if r.URL.Query().Get("tool") == "shell" {
			contribution, ok := transform.proxy.registry.contribution("shell")
			if !ok {
				http.Error(w, "shell unavailable", http.StatusBadRequest)
				return
			}
			history, err = transform.translateRegisteredTool(contribution, callID, string(body), nil)
		} else {
			history, err = transform.translate(callID, string(body), nil)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := store.put(t.Context(), transform.directory, map[string]mekugiHistory{callID: history}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		handle := ""
		if recovery := hpatchRecoveryFor(history); recovery != nil {
			handle = recovery.Handle
			recoveries[handle] = *recovery
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"carrier": history.CarrierPayload, "error": history.TranslationError, "handle": handle, "call_id": callID,
		})
	})
	// Test observation reads acknowledged durable state, never production notify().
	mux.HandleFunc("GET /progress", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		recovery, ok := recoveries[r.URL.Query().Get("handle")]
		if !ok {
			http.Error(w, "unknown fixture handle", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(transform.readHpatchRecovery(recovery))
	})
	mux.HandleFunc("POST /project", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var input struct {
			CallID string          `json:"call_id"`
			Output json.RawMessage `json:"output"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, maxReplayRecordBytes)).Decode(&input); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		request := parsedResponsesRequest{fields: map[string]json.RawMessage{"input": mustMarshalJSON([]any{
			map[string]any{"type": "custom_tool_call_output", "call_id": input.CallID, "output": input.Output},
		})}}
		visible, err := transform.proxy.reconcileVisibleInput(t.Context(), &request, transform.directory, transform.historySessionID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		projectExecutionContinuations(&mixedOutputProjection{recovery: transform.readHpatchRecovery, success: transform.projectHpatchSuccess},
			&request, continuationTestCatalog(), "exec", visible)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(request.fields["input"])
	})
	mux.HandleFunc("POST /close", func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(done) })
	})
	server := &http.Server{Handler: mux}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	metadata, err := json.Marshal(map[string]string{
		"url": "http://" + listener.Addr().String(), "root": transform.directory, "wrapper": wrapper,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "fixture.json"), metadata, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-t.Context().Done():
		t.Fatal("native fixture client did not close the bridge")
	}
}

func TestHpatchResumeCompletedNoopDoesNotRepeatShell(t *testing.T) {
	t.Parallel()
	transform, overrides := mixedTestTransform(t)
	history, err := transform.translate("noop-resume", "in existing.txt\ntype \"same\" \"same\"\nshell printf x >> count", nil)
	if err != nil || history.TranslationError != "" {
		t.Fatalf("translate = %v, %s", err, history.TranslationError)
	}
	// Exercise the exact durable boundary after segment_completed but before
	// between_segments, as if the original cell had been killed at that point.
	var config struct {
		State hpatchResumeState `json:"state"`
	}
	start := len("const mixedConfig = ")
	end := start
	for end < len(history.CarrierPayload) && history.CarrierPayload[end] != '\n' {
		end++
	}
	if err := json.Unmarshal([]byte(history.CarrierPayload[start:end-1]), &config); err != nil {
		t.Fatal(err)
	}
	config.State.Progress["current"] = mustMarshalJSON(map[string]any{
		"segment": 1, "line": 1, "kind": "edit", "status": "completed", "phase": "segment_completed",
	})
	config.State.Progress["operations"] = mustMarshalJSON([]any{})
	carrier := transform.mixedCarrier(config.State, "", nil)
	var result mixedScriptResult
	runShellCatJavaScript(t, transform.proxy.registry.NodeExecutable, transform.directory, carrier, &result, overrides)
	actual, err := os.ReadFile(filepath.Join(transform.directory, "count"))
	if err != nil || string(actual) != "x" || result.Sequence.Stopped != "" {
		t.Fatalf("shell replayed after completed no-op: %q, %v, %+v", actual, err, result)
	}
}
