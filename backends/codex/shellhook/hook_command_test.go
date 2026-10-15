package shellhook

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestHookConfigCommandAdapter(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "space and ' quote")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, script string
		blocked      bool
	}{
		{"success", "#!/bin/sh\nprintf '%s\\n' \"$@\"\ncat\n", false},
		{"nonzero", "#!/bin/sh\nexit 7\n", true},
		{"missing", "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			helper := filepath.Join(directory, tt.name+" helper's")
			if tt.script != "" {
				if err := os.WriteFile(helper, []byte(tt.script), 0700); err != nil {
					t.Fatal(err)
				}
			}
			resources := []string{filepath.Join(directory, "guard's"), "", filepath.Join(directory, "tracker's")}
			config, _, _, err := Config(helper, resources[0], resources[1], resources[2])
			if err != nil {
				t.Fatal(err)
			}
			var parsed struct {
				Hooks struct {
					PreToolUse []struct{ Hooks []struct{ Command string } }
				}
			}
			if _, err := toml.Decode(config, &parsed); err != nil {
				t.Fatal(err)
			}
			if len(parsed.Hooks.PreToolUse) != 1 || len(parsed.Hooks.PreToolUse[0].Hooks) != 1 {
				t.Fatalf("unexpected hook config: %s", config)
			}
			command := exec.CommandContext(t.Context(), "/bin/sh", "-c", parsed.Hooks.PreToolUse[0].Hooks[0].Command)
			command.Stdin = strings.NewReader("native input\n")
			var output, stderr bytes.Buffer
			command.Stdout, command.Stderr = &output, &stderr
			err = command.Run()
			if tt.blocked {
				if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 2 || stderr.Len() == 0 {
					t.Fatalf("error=%v stdout=%s stderr=%s", err, &output, &stderr)
				}
			} else {
				expected := strings.Join(append([]string{Flag}, resources...), "\n") + "\nnative input\n"
				if err != nil || output.String() != expected || stderr.Len() != 0 {
					t.Fatalf("error=%v stdout=%q want=%q stderr=%s", err, output.String(), expected, &stderr)
				}
			}
		})
	}
}
