package shellhook

import (
	"bytes"

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
		unavailable  bool
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
			resources := []string{filepath.Join(directory, "tracker's")}
			config, _, _, err := Config(helper, resources[0], "", Invocation{})
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
			if tt.unavailable {
				if err != nil || output.Len() != 0 {
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

func TestCaptureHookFailureLeavesPatchesAvailable(t *testing.T) {
	config, _, _, err := Config(filepath.Join(t.TempDir(), "missing-helper"), "", "/capture", Invocation{})
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Hooks struct {
			PreToolUse []struct {
				Matcher string
				Hooks   []struct{ Command string }
			}
		}
	}
	if _, err := toml.Decode(config, &parsed); err != nil {
		t.Fatal(err)
	}
	groups := parsed.Hooks.PreToolUse
	if len(groups) != 2 || groups[0].Matcher != "Bash|MekugiCommandTracking" || groups[1].Matcher != "^apply_patch$" {
		t.Fatalf("unexpected hook config: %s", config)
	}
	command := exec.CommandContext(t.Context(), "/bin/sh", "-c", groups[1].Hooks[0].Command)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("missing capture helper unavailable a patch: %v %s", err, output)
	}
}
