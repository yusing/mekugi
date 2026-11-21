package launch

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestChildReplySummaryGuidanceInvocation(t *testing.T) {
	for _, tc := range []struct {
		name, settings string
		args, expected []string
	}{
		{name: "unset"},
		{name: "disabled via settings", settings: "false", expected: []string{"false"}},
		{name: "disabled via CLI", settings: "true", args: []string{"--child-reply-summary=false"}, expected: []string{"false"}},
		{name: "enabled via CLI", settings: "false", args: []string{"--child-reply-summary=true"}, expected: []string{"true"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// darwin's os.UserConfigDir ignores XDG_CONFIG_HOME and derives from HOME.
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", home)
			if tc.settings != "" {
				config, err := os.UserConfigDir()
				if err != nil {
					t.Fatal(err)
				}
				directory := filepath.Join(config, "mekugi")
				if err := os.MkdirAll(directory, 0o700); err != nil {
					t.Fatal(err)
				}
				body := "[features]\nchild_reply_summary = " + tc.settings + "\n"
				if err := os.WriteFile(filepath.Join(directory, "config.toml"), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			ready, done := make(chan invocationFixture, 1), make(chan error, 1)
			go func() {
				done <- testRunSession(testHistory(), ctx, testCommands(), tc.args, nil,
					func(session invocationFixture) { ready <- session }, nil)
			}()
			var session invocationFixture
			select {
			case session = <-ready:
			case err := <-done:
				t.Fatalf("app server stopped before readiness: %v", err)
			case <-ctx.Done():
				t.Fatal("app server did not become ready")
			}
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(session.ChildReplySummaryOverrides, tc.expected) {
				t.Fatalf("native overrides = %v, want %v", session.ChildReplySummaryOverrides, tc.expected)
			}
		})
	}
}
