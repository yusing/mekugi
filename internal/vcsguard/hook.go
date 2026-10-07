package vcsguard

import (
	"crypto/sha256"
	"encoding/hex"
	json "encoding/json/v2"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/yusing/mekugi/internal/execsegment"
	"github.com/yusing/mekugi/internal/shellsyntax"
)

const HookKey = "/<session-flags>/config.toml:pre_tool_use:0:0"

const HookEnvironment = "MEKUGI_VCS_GUARD_HOOK"

func HookCommand(helper, directory string) string {
	return shellsyntax.Quote(helper) + " --vcs-hook " + shellsyntax.Quote(directory)
}

// HookConfig uses the native session-layer hook and exact trust identity. It
// neither changes user files nor grants sandbox permissions.
func HookConfig(helper, directory string) (config, state string, err error) {
	command := HookCommand(helper, directory)
	identity := map[string]any{
		"event_name": "pre_tool_use", "matcher": "^Bash$",
		"hooks": []map[string]any{{"type": "command", "command": command, "timeout": 5, "async": false}},
	}
	data, err := json.Marshal(identity, json.Deterministic(true))
	if err != nil {
		return "", "", err
	}
	hash := sha256.Sum256(data)
	config = fmt.Sprintf(`hooks.PreToolUse=[{matcher="^Bash$",hooks=[{type="command",command=%q,timeout=5}]}]`, command)
	state = strconv.Quote(HookKey) + "={trusted_hash=" + strconv.Quote("sha256:"+hex.EncodeToString(hash[:])) + "}"
	return config, state, nil
}

// RunHook only instruments the command. Approval occurs in the shell's helper
// when the command is actually reached, never for an unexecuted branch.
func RunHook(helper, directory string, input io.Reader, output, diagnostics io.Writer) int {
	var request struct {
		Event string `json:"hook_event_name"`
		Tool  string `json:"tool_name"`
		Item  string `json:"tool_use_id"`
		Input struct {
			Command *string `json:"command"`
		} `json:"tool_input"`
	}
	fail := func(err error) int {
		fmt.Fprintln(diagnostics, "mekugi: VCS guard instrumentation failed:", err)
		return 2 // Native hook rejection, with a reason, rather than fail-open.
	}
	if err := json.UnmarshalRead(io.LimitReader(input, 4<<20), &request); err != nil {
		return fail(err)
	}
	if request.Event != "PreToolUse" || request.Tool != "Bash" || request.Input.Command == nil {
		return fail(fmt.Errorf("unexpected hook input"))
	}
	changed := *request.Input.Command
	if directory != "" {
		var err error
		changed, err = RewriteForItem(changed, helper, directory, request.Item)
		if err != nil {
			return fail(err)
		}
	}
	if tracker := os.Getenv(execsegment.ShTrackerEnvironment); tracker != "" {
		if info, err := os.Stat(tracker); err == nil && info.Mode().IsRegular() {
			changed = execsegment.ShScript(tracker, changed)
		}
	}
	var response any = map[string]any{}
	if changed != *request.Input.Command {
		response = map[string]any{"hookSpecificOutput": map[string]any{
			"hookEventName": "PreToolUse", "permissionDecision": "allow",
			"updatedInput": map[string]string{"command": changed},
		}}
	}
	if err := json.MarshalWrite(output, response); err != nil {
		return fail(err)
	}
	return 0
}
