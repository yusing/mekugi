package router

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
)

// Exercise the real retained descriptor, control subprocess, durable store,
// publisher HTTP connection and terminal consumer, not just a store callback.
func TestLiveDiffMixedWorkerToPane(t *testing.T) {
	transform, overrides := mixedTestTransform(t)
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	transform.proxy.replayStore = store
	connection, broker, _ := liveDiffTestBroker(t, store, liveDiffScope{
		Workspaces: map[string]map[string]bool{transform.directory: {transform.shellThreadID: true}},
	})
	transform.proxy.autoLiveDiff = &autoLiveDiff{events: broker}
	transform.proxy.autoLiveDiff.enabled.Store(true)
	source := "new notes.txt\ntype \"mixed-one\\n\"\nshell sleep 2; printf done > shell-finished\nin notes.txt\ntype \"mixed-one\" \"mixed-two\""
	history, err := transform.translate("mixed-live", source, map[string]json.RawMessage{
		"type": mustMarshalJSON("custom_tool_call"), "name": mustMarshalJSON(mekugiToolName),
		"call_id": mustMarshalJSON("mixed-live"), "input": mustMarshalJSON(source),
	})
	if err != nil || history.TranslationError != "" {
		t.Fatalf("translation: %v %s", err, history.TranslationError)
	}
	// Hold host application until its prepared frame has reached the pane.
	// This also proves the publisher connection starts before application.
	apply := make(chan struct{}, 2)
	gate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-apply:
		case <-r.Context().Done():
		case <-time.After(15 * time.Second):
			t.Error("host application waited for a missing prepared frame")
		}
	}))
	defer gate.Close()
	defer func() { close(apply) }()
	overrides += `
const applyMixedPatch = tools.apply_patch;
tools.apply_patch = async patch => {
  await new Promise((resolve, reject) => {
    require('node:http').get(` + string(mustMarshalJSON(gate.URL)) + `, response => {
      response.resume();
      response.on('end', resolve);
    }).on('error', reject);
  });
  return applyMixedPatch(patch);
};`
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLiveDiffTerminalProcess$")
	cmd.Env = append(os.Environ(), "MEKUGI_LIVE_DIFF_TEST_CHILD=1",
		"MEKUGI_LIVE_DIFF_WORKSPACE="+transform.directory,
		"MEKUGI_LIVE_DIFF_REPLAY="+store.directory, "MEKUGI_LIVE_DIFF_SESSION="+connection)
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 10, Cols: 110})
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	viewDone := make(chan error, 1)
	go func() { viewDone <- cmd.Wait() }()
	defer func() { cancel(); <-viewDone }()
	chunks := make(chan string, 64)
	go func() {
		defer close(chunks)
		var buffer [8192]byte
		for {
			n, err := terminal.Read(buffer[:])
			if n > 0 {
				select {
				case chunks <- string(buffer[:n]):
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	pending := ""
	waitFrame := func(want string, prepared bool) string {
		t.Helper()
		for {
			endMarker := "q quit\x1b[0m"
			if end := strings.Index(pending, endMarker); end >= 0 {
				end += len(endMarker)
				frame := ansi.Strip(pending[:end])
				pending = pending[end:]
				if strings.Contains(frame, want) && strings.Contains(frame, "application unconfirmed") == prepared {
					return frame
				}
				continue
			}
			select {
			case text, open := <-chunks:
				if !open {
					t.Fatalf("pane exited before %q: %q", want, pending)
				}
				pending += text
			case <-ctx.Done():
				t.Fatalf("pane did not show %q: %q", want, pending)
			}
		}
	}
	waitFrame("edit publisher connecting", false)
	completed := make(chan struct{})
	var result mixedScriptResult
	go func() {
		defer close(completed)
		runShellCatJavaScript(t, transform.proxy.registry.NodeExecutable, transform.directory, history.carrierInput(), &result, overrides)
	}()
	started := time.Now()
	waitFrame("+mixed-one", true)
	apply <- struct{}{}
	frame := waitFrame("+mixed-one", false)
	if !strings.Contains(frame, "FOLLOW") {
		t.Fatalf("pane stopped following a real worker: %s", frame)
	}
	content, err := os.ReadFile(filepath.Join(transform.directory, "notes.txt"))
	if err != nil || string(content) != "mixed-one\n" {
		t.Fatalf("applied pane frame disagrees with actual host result: %q %v", content, err)
	}
	if _, err := os.Stat(filepath.Join(transform.directory, "shell-finished")); !os.IsNotExist(err) {
		t.Fatal("first applied frame reached the pane only after later slow shell work finished")
	}
	select {
	case <-completed:
		t.Fatal("first segment reached the pane only after the mixed script ended")
	default:
	}
	t.Logf("retained carrier start to first applied pane frame: %s (later shell sleeps 2s)", time.Since(started))
	waitFrame("+mixed-two", true)
	apply <- struct{}{}
	waitFrame("+mixed-two", false)
	select {
	case <-completed:
	case <-ctx.Done():
		t.Fatal("mixed worker did not finish")
	}
	if result.Sequence.Started != 3 || result.Sequence.Stopped != "" {
		t.Fatalf("mixed execution: %+v", result)
	}
	content, err = os.ReadFile(filepath.Join(transform.directory, "notes.txt"))
	if err != nil || string(content) != "mixed-two\n" {
		t.Fatalf("final host result: %q %v", content, err)
	}
}
