package main

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/vcsguard"
)

func TestGuardHookTrustedResources(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "must-not-execute")
	for _, command := range []string{"printf '%s' 'git push'", `git push "$(touch '` + marker + `')"; printf '%s' "$HOME"`} {
		t.Run(command, func(t *testing.T) {
			request := vcsguard.HookInput{Helper: "/forged/helper", Directory: "/forged/guard", SudoDirectory: "/forged/sudo", Tracker: "/forged/tracker", Event: "PreToolUse", Tool: "Bash", Item: "call"}
			request.Input.Command = &command
			input, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			var output, stderr bytes.Buffer
			if code := guardHook("/trusted/helper", []string{"/trusted/guard", "", ""}, bytes.NewReader(input), &output, &stderr); code != 0 || stderr.Len() != 0 {
				t.Fatalf("code=%d stderr=%s", code, &stderr)
			}
			request.Helper, request.Directory = "/trusted/helper", "/trusted/guard"
			request.SudoDirectory, request.Tracker = "", ""
			expected, err := vcsguard.RewriteHook(request)
			if err != nil {
				t.Fatal(err)
			}
			want, err := json.Marshal(expected)
			if err != nil {
				t.Fatal(err)
			}
			gotJSON, wantJSON := jsontext.Value(output.Bytes()), jsontext.Value(want)
			if err := errors.Join(gotJSON.Canonicalize(), wantJSON.Canonicalize()); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(gotJSON, wantJSON) {
				t.Fatalf("output=%s want=%s", gotJSON, wantJSON)
			}

			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("hook executed command expansion: %v", err)
			}
		})
	}
}

type hookBrokenIO struct{}

func (hookBrokenIO) Read([]byte) (int, error)  { return 0, errors.New("input unavailable") }
func (hookBrokenIO) Write([]byte) (int, error) { return 0, errors.New("output unavailable") }

func TestGuardHookBlocksFailures(t *testing.T) {
	const valid = `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"echo safe"}}`
	for _, tt := range []struct {
		name   string
		args   []string
		input  io.Reader
		output io.Writer
		reason string
	}{
		{"arguments", nil, strings.NewReader(valid), io.Discard, "expected guard"},
		{"malformed", []string{"", "", ""}, strings.NewReader("{"), io.Discard, "instrumentation failed"},
		{"oversize", []string{"", "", ""}, strings.NewReader(strings.Repeat(" ", (4<<20)+1)), io.Discard, "exceeds 4 MiB"},
		{"read", []string{"", "", ""}, hookBrokenIO{}, io.Discard, "input unavailable"},
		{"event", []string{"", "", ""}, strings.NewReader(`{"hook_event_name":"PostToolUse"}`), io.Discard, "unexpected hook input"},
		{"write", []string{"", "", ""}, strings.NewReader(valid), hookBrokenIO{}, "output unavailable"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			if code := guardHook("/trusted/helper", tt.args, tt.input, tt.output, &stderr); code != 2 || !strings.Contains(stderr.String(), tt.reason) {
				t.Fatalf("code=%d stderr=%s", code, &stderr)
			}
		})
	}
}
