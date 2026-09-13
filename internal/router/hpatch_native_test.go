package router

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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
	if err := os.WriteFile(filepath.Join(transform.directory, "native-blocker"), []byte("regular file, not a directory\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wrapper, err := exec.LookPath("shell")
	if err != nil {
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
		history, err := transform.translate(fmt.Sprintf("native-%d", count), string(body), nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"carrier": history.carrierPayload, "error": history.translationError,
		})
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
	if err != nil || history.translationError != "" {
		t.Fatalf("translate = %v, %s", err, history.translationError)
	}
	// Exercise the exact durable boundary after segment_completed but before
	// between_segments, as if the original cell had been killed at that point.
	var config struct {
		State hpatchResumeState `json:"state"`
	}
	start := len("const mixedConfig = ")
	end := start
	for end < len(history.carrierPayload) && history.carrierPayload[end] != '\n' {
		end++
	}
	if err := json.Unmarshal([]byte(history.carrierPayload[start:end-1]), &config); err != nil {
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
