package router

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestLiveDiffTerminalCatWriteStreamsDiff(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	connection, broker, _ := liveDiffTestBroker(t, store, liveDiffScope{
		Workspaces: map[string]map[string]bool{workspace: {"thread": true}},
	})
	sub := broker.subscribe()
	<-sub.events
	ui := startLiveDiffTerminal(t, workspace, store.directory, connection, 22)
	ui.frame(t, func(frame string) bool { return strings.Contains(frame, "STREAM · v diff") })
	worker := startLiveDiffPreview(t.Context(), broker, workspace, "thread")
	t.Cleanup(worker.stop)
	for i, delta := range []string{"mkdir -p generated\ncat >'visible file.txt' <<'END'\nfirst", "\nsecond", "\nEND\n"} {
		worker.appendDelta(delta)
		preview := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
			return preview.Status == "STREAMING PREVIEW" && len(preview.Files) == 1
		})
		want := "+first"
		if i > 0 {
			want = "+second"
		}
		frame := ui.frame(t, func(frame string) bool {
			text := ansi.Strip(frame)
			return strings.Contains(text, "STREAMING PREVIEW") && strings.Contains(text, want)
		})
		text := ansi.Strip(frame)
		if !strings.Contains(text, "visible file.txt") || strings.Contains(text, "cat >") || strings.Contains(text, "mkdir -p") {
			t.Fatalf("terminal did not show file diff: %q", text)
		}
		if len(preview.ID) >= 6 && strings.Contains(text, preview.ID[:6]) {
			t.Fatalf("terminal leaked preview ID prefix: %q", text)
		}
	}
	worker.stop()
	ui.frame(t, func(frame string) bool { return strings.Contains(frame, "STREAMING COMPLETE") })
	if _, err := os.Stat(filepath.Join(workspace, "visible file.txt")); !os.IsNotExist(err) {
		t.Fatalf("terminal preview changed workspace: %v", err)
	}
	ui.quit(t)
}
