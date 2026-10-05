package vcsguard

import (
	"bytes"
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunHookNativeUpdatedInput(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "must-not-execute")
	command := `git push "$(touch '` + marker + `')"; printf '%s' "$HOME"`
	request, err := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": map[string]any{"command": command, "workdir": "/workspace"}})
	if err != nil {
		t.Fatal(err)
	}
	var output, diagnostics bytes.Buffer
	if code := RunHook("/private/helper", "/private/guard", bytes.NewReader(request), &output, &diagnostics); code != 0 {
		t.Fatalf("hook code %d: %s", code, &diagnostics)
	}
	var response struct {
		Output struct {
			Event    string `json:"hookEventName"`
			Decision string `json:"permissionDecision"`
			Input    struct {
				Command string `json:"command"`
			} `json:"updatedInput"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	expected, err := Rewrite(command, "/private/helper", "/private/guard")
	if err != nil {
		t.Fatal(err)
	}
	if response.Output.Event != "PreToolUse" || response.Output.Decision != "allow" || response.Output.Input.Command != expected || expected == command {
		t.Fatalf("invalid native updatedInput: %s", &output)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("rewrite executed expansion: %v", err)
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("unexpected diagnostics: %s", &diagnostics)
	}
}

func TestRunHookNoChangeAndInvalidInput(t *testing.T) {
	for _, tt := range []struct {
		name, input string
		code        int
	}{
		{"ordinary command", `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"printf '%s' 'git push'"}}`, 0},
		{"malformed JSON", `{`, 2},
		{"wrong event", `{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"git push"}}`, 2},
		{"wrong tool", `{"hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"command":"git push"}}`, 2},
		{"missing command", `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{}}`, 2},
		{"invalid shell", `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"git push '"}}`, 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var output, diagnostics bytes.Buffer
			if code := RunHook("/helper", "/guard", strings.NewReader(tt.input), &output, &diagnostics); code != tt.code {
				t.Fatalf("code=%d diagnostics=%s", code, &diagnostics)
			}
			if tt.code == 0 {
				if output.String() != "{}" || diagnostics.Len() != 0 {
					t.Fatalf("unchanged command output=%q diagnostics=%q", &output, &diagnostics)
				}
			} else if output.Len() != 0 || diagnostics.Len() == 0 {
				t.Fatalf("rejected input output=%q diagnostics=%q", &output, &diagnostics)
			}
		})
	}
}
