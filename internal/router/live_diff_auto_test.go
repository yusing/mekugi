package router

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func autoLiveDiffFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("PATH", dir)
	log := filepath.Join(dir, "calls")
	t.Setenv("MEKUGI_AUTO_DIFF_LOG", log)
	for name, script := range map[string]string{
		"herdr": `#!/bin/sh
printf '%s\n' "$@" >> "$MEKUGI_AUTO_DIFF_LOG"
case "$2" in
split) printf '%s\n' '{"result":{"pane":{"pane_id":"new"}}}' ;;
run) printf '%s\n' done >> "$MEKUGI_AUTO_DIFF_LOG" ;;
esac
`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	return log
}

func waitAutoLiveDiff(t *testing.T, log, marker string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for {
		data, _ := os.ReadFile(log)
		if strings.Contains(string(data), marker) {
			return string(data)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("missing %q in Herdr log %q", marker, data)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestAutoLiveDiffSelectedWorkspaceBoundary(t *testing.T) {
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	log := autoLiveDiffFixture(t)
	workspace := t.TempDir()
	alias := filepath.Join(t.TempDir(), "selected")
	if err := os.Symlink(workspace, alias); err != nil {
		t.Fatal(err)
	}
	auto, stop := newAutoLiveDiff(t.Context(), t.TempDir())
	defer stop()
	proxy.autoLiveDiff = auto
	auto.enable()
	request := serverRequest(t, nil)
	headers := serverMetadataHeaders(t, "turn", map[string]json.RawMessage{alias: nil})
	provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(`{"id":"r","status":"completed","output":[]}`)}}}
	if err := executeRequest(t.Context(), t.Context(), request, headers, "auto", provider, io.Discard, nil, proxy, nil, nil); err != nil {
		t.Fatal(err)
	}
	data := waitAutoLiveDiff(t, log, "\ndone\n")
	if !strings.Contains(data, "\nsplit\n--current\n--direction\nright\n--cwd\n"+workspace+"\n--no-focus\n") ||
		!strings.Contains(data, " live-diff --workspace "+shellQuoteArgument(workspace)) ||
		!strings.Contains(data, "\nrun\nnew\n") {
		t.Fatalf("incorrect selected workspace or launch: %s", data)
	}
	if strings.Contains(data, "\nlayout\n") || strings.Contains(data, "\ncurrent\n") {
		t.Fatal("launch queried geometry instead of directly splitting the caller to the right")
	}
	var callers sync.WaitGroup
	for range 20 {
		callers.Go(func() { auto.observe(workspace, "thread-1", codexTurnMetadata{RequestKind: "turn"}) })
	}
	callers.Wait()
	stop()
	dataBytes, _ := os.ReadFile(log)
	if strings.Count(string(dataBytes), "\nsplit\n") != 1 {
		t.Fatal("subsequent turns created duplicate panes")
	}
}

func TestAutoLiveDiffEligibility(t *testing.T) {
	for _, name := range []string{"disabled", "outside", "missing_herdr", "empty", "child", "auxiliary"} {
		t.Run(name, func(t *testing.T) {
			log := autoLiveDiffFixture(t)
			a, stop := newAutoLiveDiff(t.Context(), t.TempDir())
			workspace := t.TempDir()
			metadata := codexTurnMetadata{RequestKind: "turn"}
			switch name {
			case "outside":
				t.Setenv("HERDR_ENV", "")
			case "missing_herdr":
				binary := strings.TrimPrefix(name, "missing_")
				if err := os.Remove(filepath.Join(os.Getenv("PATH"), binary)); err != nil {
					t.Fatal(err)
				}
			case "empty":
				workspace = ""
			case "child":
				metadata.SubagentKind = "review"
			case "auxiliary":
				metadata.RequestKind = "compact"
			}
			if name != "disabled" {
				a.enable()
			}
			a.observe(workspace, "thread-1", metadata)
			stop()
			if a.workspace != "" {
				t.Fatal("ineligible turn claimed launch")
			}
			if _, err := os.Stat(log); !os.IsNotExist(err) {
				t.Fatal("ineligible turn invoked Herdr")
			}
		})
	}
}

func TestAutoLiveDiffCancellationAndFailure(t *testing.T) {
	for _, mode := range []string{"cancel_before", "cancel_running", "failure"} {
		t.Run(mode, func(t *testing.T) {
			log := autoLiveDiffFixture(t)
			script := "#!/bin/sh\nprintf 'started\\n' >> \"$MEKUGI_AUTO_DIFF_LOG\"\n"
			if mode == "failure" {
				script += "exit 1\n"
			} else {
				script += "exec /bin/sleep 30\n"
			}
			if err := os.WriteFile(filepath.Join(os.Getenv("PATH"), "herdr"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			a, stop := newAutoLiveDiff(ctx, t.TempDir())
			a.enable()
			if mode == "cancel_before" {
				cancel()
			}
			a.observe(t.TempDir(), "thread-1", codexTurnMetadata{RequestKind: "turn"})
			if mode != "cancel_before" {
				waitAutoLiveDiff(t, log, "started")
			}
			joined := make(chan struct{})
			go func() { stop(); close(joined) }()
			select {
			case <-joined:
			case <-time.After(3 * time.Second):
				t.Fatal("auxiliary launch prevented router shutdown")
			}
			if mode == "cancel_before" {
				if _, err := os.Stat(log); !os.IsNotExist(err) {
					t.Fatal("canceled launch invoked Herdr")
				}
			}
		})
	}
}

func TestAutoLiveDiffScopeCapacity(t *testing.T) {
	a := &autoLiveDiff{
		scope:      liveDiffScope{Workspaces: make(map[string]map[string]bool)},
		scopeBytes: len(`{"Workspaces":{}}`),
		changed:    make(chan struct{}, 1),
	}
	a.enabled.Store(true)
	for _, workspace := range []string{"/tmp/a", "/tmp/with\"quote"} {
		for _, thread := range []string{"root", "child\nwith\\escape"} {
			a.observe(workspace, thread, codexTurnMetadata{RequestKind: "turn"})
			data, err := json.Marshal(a.scope)
			if err != nil || len(data) != a.scopeBytes {
				t.Fatalf("scope accounting: %d != %d (%v)", len(data), a.scopeBytes, err)
			}
		}
	}
	a.observe("/tmp/oversized", strings.Repeat("x", maxLiveDiffScopeBytes), codexTurnMetadata{RequestKind: "turn"})
	if a.enabled.Load() || a.scope.Workspaces != nil {
		t.Fatal("scope exhaustion did not disable/release auxiliary collection")
	}
	a.observe("/tmp/after", "new", codexTurnMetadata{RequestKind: "turn"})
	if a.scope.Workspaces != nil {
		t.Fatal("disabled collection resumed")
	}
}
