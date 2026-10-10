package vcsguard

import (
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"testing"
)

func TestRewriteHookNativeUpdatedInput(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "must-not-execute")
	command := `git push "$(touch '` + marker + `')"; printf '%s' "$HOME"`
	input := HookInput{Helper: "/private/helper", Directory: "/private/guard", Event: "PreToolUse", Tool: "Bash", Item: "cmd"}
	input.Input.Command = &command
	result, err := RewriteHook(input)
	if err != nil {
		t.Fatal(err)
	}
	output, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
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
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatal(err)
	}
	expected, err := RewriteForItem(command, "/private/helper", "/private/guard", "cmd")
	if err != nil {
		t.Fatal(err)
	}
	if response.Output.Event != "PreToolUse" || response.Output.Decision != "allow" || response.Output.Input.Command != expected || expected == command {
		t.Fatalf("invalid native updatedInput: %s", output)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("rewrite executed expansion: %v", err)
	}
}

func TestRewriteHookNoChangeAndInvalidInput(t *testing.T) {
	for _, tt := range []struct {
		name, input string
		fail        bool
	}{
		{"ordinary command", `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"printf '%s' 'git push'"}}`, false},
		{"malformed JSON", `{`, true},
		{"wrong event", `{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"git push"}}`, true},
		{"wrong tool", `{"hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"command":"git push"}}`, true},
		{"missing command", `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{}}`, true},
		{"invalid shell", `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"git push '"}}`, true},
		{"relative helper", `{"helper":"relative/helper","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"git push"}}`, true},
		{"relative resource", `{"directory":"relative/guard","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"git push"}}`, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var input HookInput
			err := json.Unmarshal([]byte(tt.input), &input)
			if input.Helper == "" {
				input.Helper = "/helper"
			}
			if input.Directory == "" {
				input.Directory = "/guard"
			}
			var output any
			if err == nil {
				output, err = RewriteHook(input)
			}
			if (err != nil) != tt.fail {
				t.Fatalf("output=%v error=%v", output, err)
			}
			data, marshalErr := json.Marshal(output)
			if err == nil && (marshalErr != nil || string(data) != "{}") {
				t.Fatalf("unchanged output=%v", output)
			}
		})
	}
}
