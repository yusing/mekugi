package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestClaudeCLIHelpAndValidation(t *testing.T) {
	for _, tc := range []struct {
		args []string
		code int
		want string
	}{{[]string{"--help"}, 0, "make build-claude"}, {[]string{"--unknown"}, 2, "flag provided but not defined"}, {[]string{"extra"}, 2, "unexpected positional"}, {nil, 1, "interactive terminal required"}} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			in, err := os.Open(os.DevNull)
			if err != nil {
				t.Fatal(err)
			}
			defer in.Close()
			out, err := os.CreateTemp(t.TempDir(), "output")
			if err != nil {
				t.Fatal(err)
			}
			defer out.Close()
			var diagnostics bytes.Buffer
			if code := runClaude(t.Context(), tc.args, in, out, &diagnostics); code != tc.code || !strings.Contains(diagnostics.String(), tc.want) {
				t.Fatalf("code=%d output=%q", code, diagnostics.String())
			}
		})
	}
}
