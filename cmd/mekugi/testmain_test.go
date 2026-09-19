package main

import (
	"fmt"
	"os"
	"testing"
)

// Provider selection must not depend on credentials in the developer's shell.
// Empty overrides also disable credentials from the local configuration file.
func TestMain(m *testing.M) {
	for _, name := range []string{"OPENCODE_API_KEY", "OPENCODE_GO_API_KEY", "OPENCODE_ZEN_API_KEY"} {
		if err := os.Setenv(name, ""); err != nil {
			fmt.Fprintf(os.Stderr, "isolate test environment %s: %v\n", name, err)
			os.Exit(1)
		}
	}
	os.Exit(m.Run())
}
