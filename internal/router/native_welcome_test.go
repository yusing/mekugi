package router

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
)

func TestNativeWelcome(t *testing.T) {
	for _, tc := range []struct{ agent, want string }{
		{"codex_cli_rs/0.158.0 (Linux 6; x86_64) terminal", "0.158.0"},
		{"mekugi/0.158.0-alpha.1 (Linux)", "0.158.0-alpha.1"}, {"", ""}, {"invalid", ""}, {"codex/evil\x1b[2J", ""},
	} {
		if got := backendVersion(tc.agent); got != tc.want {
			t.Fatalf("%q: %q", tc.agent, got)
		}
	}
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.backendVersion = "0.158.0"
	rows, _ := u.mainFrame(80, 24, 0)
	if !strings.Contains(ansi.Strip(rows[0]), "Mekugi "+mekugiVersion()+" • codex v0.158.0") {
		t.Fatalf("welcome %q", rows[0])
	}
	if len(u.view.entries) != 0 {
		t.Fatal("welcome entered history")
	}
}

func TestUISnapshotNativeWelcome(t *testing.T) {
	for _, tc := range []struct {
		name          string
		width, height int
		draft         string
	}{
		{"launch", 80, 24, ""}, {"first-draft", 80, 24, "Explain this repository."},
		{"compact", 36, 8, "Explain this repository."},
		{"full-composer", 36, 6, "first\nsecond\nthird\nfourth\nfifth"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			u.view.painter.Theme = livediff.DarkTheme
			u.backendVersion, u.status, u.model, u.draft = "0.158.0", "Ready", "snapshot-model", tc.draft
			rows, _ := u.mainFrame(tc.width, tc.height, 0)
			// Build identity is not fixed across test executables.
			rows[0] = strings.ReplaceAll(rows[0], "Mekugi "+mekugiVersion(), "Mekugi dev")
			assertNativeUISnapshot(t, "native-welcome-"+tc.name, rows)
		})
	}
}

func TestNativeWelcomeDoesNotReplaceHistoryOrLiveDock(t *testing.T) {
	for _, dock := range []int{0, 12} {
		t.Run(fmt.Sprint(dock), func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			if dock == 0 {
				u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: 1, Agent: "You", Kind: "text", Text: "Submitted prompt"}}})
			}
			rows, _ := u.mainFrame(80, 24, dock)
			if strings.Contains(strings.Join(rows, "\n"), "Mekugi "+mekugiVersion()) {
				t.Fatal("welcome replaced active content")
			}
		})
	}
}
