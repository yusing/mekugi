package main

import (
	"fmt"
	"os"
	"testing"
)

// Provider selection must not depend on credentials in the developer's shell.
// Empty overrides also disable credentials from the local configuration file.
func TestMain(m *testing.M) {
	// Generated debug bundles and retention must never use the developer's state.
	// Child fixtures keep the explicit state root selected by their parent.
	var stateDirectory string
	if os.Getenv("MEKUGI_LAUNCHER_TEST_STATE_ISOLATED") == "" {
		var err error
		stateDirectory, err = os.MkdirTemp("", "mekugi-launcher-test-state-")
		if err == nil {
			err = os.Setenv("XDG_STATE_HOME", stateDirectory)
		}
		if err == nil {
			err = os.Setenv("MEKUGI_LAUNCHER_TEST_STATE_ISOLATED", "1")
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	for _, name := range []string{"OPENCODE_API_KEY", "OPENCODE_GO_API_KEY", "OPENCODE_ZEN_API_KEY"} {
		if err := os.Setenv(name, ""); err != nil {
			fmt.Fprintf(os.Stderr, "isolate test environment %s: %v\n", name, err)
			os.Exit(1)
		}
	}
	code := m.Run()
	if stateDirectory != "" {
		if err := os.RemoveAll(stateDirectory); err != nil {
			fmt.Fprintln(os.Stderr, err)
			code = 1
		}
	}
	os.Exit(code)
}
