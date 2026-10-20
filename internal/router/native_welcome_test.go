package router

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/livediff"
)

func TestNativeWelcome(t *testing.T) {
	setMekugiBuildVersion(t, "")
	for _, tc := range []struct{ agent, want string }{
		{"codex_cli_rs/0.158.0 (Linux 6; x86_64) terminal", "0.158.0"},
		{"mekugi/0.158.0-alpha.1 (Linux)", "0.158.0-alpha.1"}, {"", ""}, {"invalid", ""}, {"codex/evil\x1b[2J", ""},
	} {
		if got := backendVersion(tc.agent); got != tc.want {
			t.Fatalf("%q: %q", tc.agent, got)
		}
	}
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.mainFrame(80, 24, 0)
	if len(u.view.entries) != 0 {
		t.Fatal("welcome entered history")
	}
}

func setMekugiBuildVersion(t *testing.T, version string) {
	t.Helper()
	previous := buildVersion
	buildVersion = version
	t.Cleanup(func() { buildVersion = previous })
}

func TestMekugiBuildVersion(t *testing.T) {
	setMekugiBuildVersion(t, "v1.2.3")
	if got := mekugiVersion(); got != "v1.2.3" {
		t.Fatalf("build version = %q, want v1.2.3", got)
	}
}

func TestMekugiLinkedBuildVersion(t *testing.T) {
	want := os.Getenv("MEKUGI_TEST_BUILD_VERSION")
	if want == "" {
		t.Skip("requires a version injected with -ldflags -X")
	}
	if got := mekugiVersion(); got != want {
		t.Fatalf("linked build version = %q, want %q", got, want)
	}
}

func TestUISnapshotNativeWelcome(t *testing.T) {
	for _, tc := range []struct {
		name          string
		width, height int
		draft         string
		version       string
	}{
		{"launch", 80, 24, "", ""}, {"first-draft", 80, 24, "Explain this repository.", ""},
		{"compact", 36, 8, "Explain this repository.", ""},
		{"full-composer", 36, 6, "first\nsecond\nthird\nfourth\nfifth", ""},
		{"release", 80, 24, "", "v1.2.3"},
		{"release-compact", 36, 8, "Explain this repository.", "v1.2.3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setMekugiBuildVersion(t, tc.version)
			u := newAppServerSessionTestUI(t, t.TempDir())
			u.view.painter.Theme = livediff.DarkTheme
			u.backendVersion, u.status, u.model, u.draft = "0.158.0", "Ready", "snapshot-model", tc.draft
			rows, _ := u.mainFrame(tc.width, tc.height, 0)
			// Build identity is not fixed across test executables.
			if tc.version == "" {
				rows[0] = strings.ReplaceAll(rows[0], "Mekugi "+mekugiVersion(), "Mekugi dev")
			}
			assertNativeUISnapshot(t, "native-welcome-"+tc.name, rows)
		})
	}
}

func TestNativeWelcomeDoesNotReplaceHistoryOrLiveDock(t *testing.T) {
	setMekugiBuildVersion(t, "")
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
