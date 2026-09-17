package toolplugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWarmBuiltinTranslatorBurst(t *testing.T) {
	t.Parallel()
	snapshot, err := Load(t.Context(), "", filepath.Join(t.TempDir(), "snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	plugin := snapshot.Plugins[0]
	index := -1
	for i, tool := range plugin.Tools {
		var spec struct{ Name string }
		if err := json.Unmarshal(tool.Specification, &spec); err != nil {
			t.Fatal(err)
		}
		if spec.Name == "shell" {
			index = i
		}
	}
	if index == -1 {
		t.Fatal("shell not registered")
	}
	translator, err := NewTranslator(t.Context(), snapshot.NodeExecutable, snapshot.Root, plugin.Module)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(translator.Close)
	process := translator.process
	started := time.Now()
	var calls sync.WaitGroup
	for i := range 32 {
		calls.Go(func() {
			script := fmt.Sprintf("printf 'call %d\\n'\necho second\necho third\necho fourth\n", i)
			if i%2 != 0 {
				script = fmt.Sprintf("printf 'call %d\\n'", i)
			}
			result, err := translator.Translate(t.Context(), index, script)
			if err != nil {
				t.Errorf("call %d: %v", i, err)
				return
			}
			if result.Rejected || result.Carrier.Kind != "exec" || !slices.Equal(result.Arguments, []string{"bash", script}) {
				t.Errorf("call %d: unexpected translation: %+v", i, result)
			}
		})
	}
	calls.Wait()
	if translator.process != process {
		t.Fatal("burst replaced the warm process")
	}
	t.Logf("32 warm translations completed in %s", time.Since(started))
}

func newFixtureTranslator(t *testing.T) *Translator {
	t.Helper()
	// This fixture imports no built-ins. Exercise the real translation host
	// without rebuilding and validating an unused Node/WASM reader catalog.
	root := t.TempDir()
	node, err := resolveNodeRuntime(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	host, err := runtimeFiles.ReadFile(hostFilename)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, hostFilename), host, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, snapshotDirectory), 0o700); err != nil {
		t.Fatal(err)
	}
	module := `import {appendFileSync, writeFileSync} from "node:fs";
appendFileSync(new URL("./imports", import.meta.url), "loaded\n");
export default {apiVersion: "mekugi-tool-plugin/v1", tools: [{
  parse(input) { if (input === "reject") throw new Error("invalid input"); return input; },
  argv(input) { return [input]; },
  translate(input, api) {
    if (input === "hang") { writeFileSync(new URL("./active", import.meta.url), "active"); while (true) {} }
    if (input === "crash") process.exit(7);
    if (input === "malformed") return {kind: "unknown"};
    if (input === "null") process.stdout.write("null\n");
    if (input === "overflow") process.stdout.write("x".repeat(17 * 1024 * 1024));
    return api.exec();
  },
  execute() { throw new Error("executor must not run"); }
}]};`
	if err := os.WriteFile(filepath.Join(root, snapshotDirectory, "fixture.mjs"), []byte(module), 0o600); err != nil {
		t.Fatal(err)
	}
	translator, err := NewTranslator(t.Context(), node, root, "fixture.mjs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(translator.Close)
	return translator
}

func TestWarmTranslatorReusesImportsAndPreservesRejection(t *testing.T) {
	t.Parallel()
	translator := newFixtureTranslator(t)
	for _, input := range []string{"first\nline", "reject", "last"} {
		result, err := translator.Translate(t.Context(), 0, input)
		if err != nil {
			t.Fatal(err)
		}
		if input == "reject" {
			if !result.Rejected || result.Diagnostic != "invalid input" {
				t.Fatalf("rejection = %+v", result)
			}
		} else if !slices.Equal(result.Arguments, []string{input}) {
			t.Fatalf("arguments = %q", result.Arguments)
		}
	}
	imports, err := os.ReadFile(filepath.Join(translator.root, snapshotDirectory, "imports"))
	if err != nil || string(imports) != "loaded\n" {
		t.Fatalf("imports = %q, %v; want one import before serving", imports, err)
	}
}

func TestWarmTranslatorFailureDoesNotRetryAndNextCallRecovers(t *testing.T) {
	t.Parallel()
	for _, input := range []string{"hang", "crash", "malformed", "overflow", "null"} {
		t.Run(input, func(t *testing.T) {
			translator := newFixtureTranslator(t)
			process := translator.process
			ctx := t.Context()
			if input == "hang" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
				defer cancel()
			}
			_, err := translator.Translate(ctx, 0, input)
			wantError := map[string]string{
				"hang": "context deadline exceeded", "crash": "exit status 7",
				"malformed": "translator returned a malformed carrier", "overflow": "token too long", "null": "incomplete result",
			}[input]
			if err == nil || !strings.Contains(err.Error(), wantError) {
				t.Fatalf("invalid translator call: %v; want %q", err, wantError)
			}
			select {
			case <-process.done:
			default:
				t.Fatal("failed process not reaped")
			}
			imports, err := os.ReadFile(filepath.Join(translator.root, snapshotDirectory, "imports"))
			if err != nil || string(imports) != "loaded\n" {
				t.Fatalf("failed call retried: imports=%q, err=%v", imports, err)
			}
			result, err := translator.Translate(t.Context(), 0, "next")
			if err != nil || result.Rejected || result.Carrier.Kind != "exec" {
				t.Fatalf("next call = %+v, %v", result, err)
			}
		})
	}
}

func TestWarmTranslatorCloseCancelsActiveCallAndQueuedCallCanCancel(t *testing.T) {
	t.Parallel()
	translator := newFixtureTranslator(t)
	// Reserve admission to deterministically test a cancelled queued call.
	translator.gate <- struct{}{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := translator.Translate(ctx, 0, "queued"); !errors.Is(err, context.Canceled) {
		t.Fatalf("queued cancellation = %v", err)
	}
	<-translator.gate
	started := time.Now()
	result := make(chan error, 1)
	go func() {
		_, err := translator.Translate(t.Context(), 0, "hang")
		result <- err
	}()
	// Wait for the fixture to enter its non-yielding translator, not just for
	// the Go goroutine to start, before testing shutdown cancellation.
	for {
		if _, err := os.Stat(filepath.Join(translator.root, snapshotDirectory, "active")); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if time.Since(started) > 2*time.Second {
			t.Fatal("translator did not enter the active call")
		}
		time.Sleep(time.Millisecond)
	}
	translator.Close()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("close cancellation = %v", err)
	}
	if time.Since(started) >= pluginInvocationTimeout {
		t.Fatal("close waited for the translation timeout")
	}
	if _, err := translator.Translate(t.Context(), 0, "after close"); !errors.Is(err, context.Canceled) {
		t.Fatalf("closed translation = %v", err)
	}
}

func TestWarmTranslatorStartupFailure(t *testing.T) {
	t.Parallel()
	translator := newFixtureTranslator(t)
	translator.Close()
	_, err := NewTranslator(t.Context(), translator.node, translator.root, "missing.mjs")
	if err == nil || !strings.Contains(err.Error(), "missing.mjs") {
		t.Fatalf("startup failure = %v", err)
	}
}

func TestWarmTranslatorReplacesExitedIdleHost(t *testing.T) {
	t.Parallel()
	translator := newFixtureTranslator(t)
	previous := translator.process
	previous.cancel()
	<-previous.done
	result, err := translator.Translate(t.Context(), 0, "next")
	if err != nil || result.Carrier.Kind != "exec" || translator.process == previous {
		t.Fatalf("idle replacement = %+v, %v", result, err)
	}
}
