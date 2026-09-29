package router

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
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
