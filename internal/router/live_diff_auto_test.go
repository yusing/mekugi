package router

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func autoLiveDiffFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_PANE_ID", "caller")
	t.Setenv("HERDR_TAB_ID", "tab")
	t.Setenv("HERDR_WORKSPACE_ID", "workspace")
	t.Setenv("PATH", dir)
	log := filepath.Join(dir, "calls")
	t.Setenv("MEKUGI_AUTO_DIFF_LOG", log)
	socket := filepath.Join(dir, "herdr.sock")
	t.Setenv("HERDR_SOCKET_PATH", socket)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			serveAutoLiveDiffAPI(connection, log)
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-done
	})
	script := `#!/bin/sh
printf 'cli\n' >> "$MEKUGI_AUTO_DIFF_LOG"
printf '%s\n' "$@" >> "$MEKUGI_AUTO_DIFF_LOG"
`
	if err := os.WriteFile(filepath.Join(dir, "herdr"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return log
}

func serveAutoLiveDiffAPI(connection net.Conn, log string) {
	defer connection.Close()
	var request struct {
		ID     string          `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(connection).Decode(&request); err != nil {
		return
	}
	data, _ := json.Marshal(request)
	file, err := os.OpenFile(log, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
	if err == nil {
		_, _ = fmt.Fprintf(file, "api\n%s\n%s\n", request.Method, data)
		_ = file.Close()
	}
	mode := os.Getenv("MEKUGI_AUTO_DIFF_API_MODE")
	if mode != "" {
		file, err := os.OpenFile(log, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
		if err == nil {
			_, _ = file.WriteString("started\n")
			_ = file.Close()
		}
	}
	if mode == "block" {
		_, _ = io.Copy(io.Discard, connection)
		return
	}
	if mode == "failure" {
		_ = json.NewEncoder(connection).Encode(map[string]any{
			"id": request.ID, "error": map[string]string{"code": "test_failure", "message": "failed"},
		})
		return
	}
	var result any
	switch request.Method {
	case "pane.current":
		result = map[string]any{
			"type": "pane_current",
			"pane": map[string]string{"pane_id": "caller", "tab_id": "tab", "workspace_id": "workspace"},
		}
	case "layout.apply":
		layout := map[string]string{"tab_id": "temporary", "focused_pane_id": "new"}
		if mode == "missing_identity" {
			layout["focused_pane_id"] = ""
		}
		result = map[string]any{"type": "layout_apply", "layout": layout}
	case "pane.layout":
		// Without a width the tab layout is unknown, which stacks the panes.
		width, err := strconv.Atoi(os.Getenv("MEKUGI_AUTO_DIFF_TAB_WIDTH"))
		if err != nil {
			_ = json.NewEncoder(connection).Encode(map[string]any{
				"id": request.ID, "error": map[string]string{"code": "unknown_method", "message": request.Method},
			})
			return
		}
		result = map[string]any{"type": "pane_layout", "layout": map[string]any{"area": map[string]int{"width": width, "height": 60}}}
	case "pane.move":
		result = map[string]any{
			"type": "pane_move",
			"move_result": map[string]any{
				"changed": true, "closed_tab_id": "temporary", "focused_pane_id": "sibling",
				"pane": map[string]string{"pane_id": "new", "tab_id": "tab", "workspace_id": "workspace"},
			},
		}
	default:
		_ = json.NewEncoder(connection).Encode(map[string]any{
			"id": request.ID, "error": map[string]string{"code": "unknown_method", "message": request.Method},
		})
		return
	}
	_ = json.NewEncoder(connection).Encode(map[string]any{"id": request.ID, "result": result})
	if request.Method == "pane.move" {
		if file, err := os.OpenFile(log, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600); err == nil {
			_, _ = file.WriteString("done\n")
			_ = file.Close()
		}
	}
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

func TestAutoLiveDiffEligibility(t *testing.T) {
	for _, name := range []string{"disabled", "outside", "missing_herdr", "empty", "child", "auxiliary", "no_call", "unknown_thread"} {
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
			if name == "unknown_thread" {
				a.requestLaunch(workspace, "other")
			} else if name != "no_call" {
				a.requestLaunch(workspace, "thread-1")
			}
			stop()
			if a.requested && a.workspace != "" {
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
			if mode == "cancel_running" {
				t.Setenv("MEKUGI_AUTO_DIFF_API_MODE", "block")
			} else if mode == "failure" {
				t.Setenv("MEKUGI_AUTO_DIFF_API_MODE", "failure")
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			a, stop := newAutoLiveDiff(ctx, t.TempDir())
			a.enable()
			if mode == "cancel_before" {
				cancel()
			}
			workspace := t.TempDir()
			a.observe(workspace, "thread-1", codexTurnMetadata{RequestKind: "turn"})
			a.requestLaunch(workspace, "thread-1")
			if mode != "cancel_before" {
				waitAutoLiveDiff(t, log, "started\n")
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
	// This fixture exercises scope accounting without launching a pane.
	a := &autoLiveDiff{
		events:     newLiveDiffBroker(t.Context()),
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

func TestAutoLiveDiffChildPreviewWaitsForRootWorkspace(t *testing.T) {
	log := autoLiveDiffFixture(t)
	a, stop := newAutoLiveDiff(t.Context(), t.TempDir())
	defer stop()
	a.enable()
	childWorkspace, rootWorkspace := t.TempDir(), t.TempDir()
	a.observe(childWorkspace, "child", codexTurnMetadata{RequestKind: "turn", SubagentKind: "review"})
	a.requestLaunch(childWorkspace, "child")
	a.mu.Lock()
	if !a.requested || a.workspace != "" {
		a.mu.Unlock()
		t.Fatal("child preview did not defer launch until root workspace selection")
	}
	a.mu.Unlock()
	a.observe(rootWorkspace, "root", codexTurnMetadata{RequestKind: "turn"})
	data := waitAutoLiveDiff(t, log, "done\n")
	if !strings.Contains(data, `"cwd":"`+rootWorkspace+`"`) {
		t.Fatalf("child preview overrode root workspace: %s", data)
	}
}

func TestAutoLiveDiffStacksAgentsPane(t *testing.T) {
	for _, test := range []struct {
		order, width, split string
	}{
		{"diff_first", "", `"ratio":0.55,"split":"down","tab_id":"tab","target_pane_id":"new"`},
		{"agents_first", "", `"ratio":0.45,"split":"down","tab_id":"tab","target_pane_id":"new"`},
		{"diff_first", "160", `"ratio":0.55,"split":"down","tab_id":"tab","target_pane_id":"new"`},
		// Wide tabs give each pane its own full-height column. Either way the
		// diff pane keeps the larger share.
		{"diff_first", "280", `"ratio":0.55,"split":"right","tab_id":"tab","target_pane_id":"new"`},
		{"agents_first", "280", `"ratio":0.45,"split":"right","tab_id":"tab","target_pane_id":"new"`},
	} {
		order := test.order
		t.Run(order+test.width, func(t *testing.T) {
			t.Setenv("MEKUGI_AUTO_DIFF_TAB_WIDTH", test.width)
			log := autoLiveDiffFixture(t)
			a, stop := newAutoLiveDiff(t.Context(), t.TempDir())
			defer stop()
			a.activityConnection = func() liveDiffConnection { return liveDiffConnection{Endpoint: "http://127.0.0.1:1", Token: "token"} }
			a.enable()
			workspace := t.TempDir()
			a.observe(workspace, "thread-1", codexTurnMetadata{RequestKind: "turn"})
			first, second := func() { a.requestLaunch(workspace, "thread-1") }, func() {
				if !a.requestActivity() {
					t.Fatal("agents pane request was refused")
				}
			}
			if order == "agents_first" {
				first, second = second, first
			}
			first()
			waitAutoLiveDiff(t, log, "done\n")
			second()
			data := waitAutoLiveDiff(t, log, "done\n")
			for deadline := time.Now().Add(10 * time.Second); strings.Count(data, "done\n") < 2; {
				if time.Now().After(deadline) {
					t.Fatalf("second pane was not placed: %s", data)
				}
				time.Sleep(10 * time.Millisecond)
				data = waitAutoLiveDiff(t, log, "done\n")
			}
			if a.requestActivity() {
				t.Fatal("agents pane launched twice")
			}
			var moves []string
			for line := range strings.SplitSeq(data, "\n") {
				if strings.Contains(line, `"method":"pane.move"`) {
					moves = append(moves, line)
				}
			}
			if len(moves) != 2 || !strings.Contains(moves[0], `{"split":"right","tab_id":"tab","target_pane_id":"caller"`) || !strings.Contains(moves[1], test.split) {
				t.Fatalf("pane placement = %q", moves)
			}
			command := "live-diff"
			if order == "agents_first" {
				command = "live-activity"
			}
			if !strings.Contains(data[:strings.Index(data, "done\n")], command) {
				t.Fatalf("first pane was not %s: %s", command, data)
			}
		})
	}
}

func TestAutoLiveDiffAgentsPaneUnavailableAfterStop(t *testing.T) {
	autoLiveDiffFixture(t)
	a, stop := newAutoLiveDiff(t.Context(), t.TempDir())
	a.activityConnection = func() liveDiffConnection { return liveDiffConnection{Endpoint: "http://127.0.0.1:1", Token: "token"} }
	a.enable()
	if a.requestActivity() {
		t.Fatal("agents pane accepted without a root workspace")
	}
	a.observe(t.TempDir(), "thread-1", codexTurnMetadata{RequestKind: "turn"})
	stop()
	if a.requestActivity() {
		t.Fatal("agents pane accepted after the launcher stopped")
	}
}
