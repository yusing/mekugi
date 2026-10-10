package vcsguard

import (
	"crypto/sha256"
	"encoding/hex"
	json "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/yusing/mekugi/internal/execsegment"
)

const HookKey = "/<session-flags>/config.toml:pre_tool_use:0:0"

const HookEnvironment = "MEKUGI_VCS_GUARD_HOOK"

const HookServer = "mekugi"
const HookTool = "guard_rewrite"

// HookInput binds session resources in the trusted hook configuration. The
// remaining fields are expanded from Codex's native hook event, not router cwd.
type HookInput struct {
	Helper        string `json:"helper"`
	Directory     string `json:"directory"`
	SudoDirectory string `json:"sudo_directory"`
	Tracker       string `json:"tracker"`
	Event         string `json:"hook_event_name"`
	Tool          string `json:"tool_name"`
	Item          string `json:"tool_use_id"`
	Input         struct {
		Command *string `json:"command"`
	} `json:"tool_input"`
}

// HookConfig uses the native session-layer hook and exact trust identity. It
// neither changes user files nor grants sandbox permissions.
func HookConfig(helper, directory, sudoDirectory, tracker string) (config, state, identityHash string, err error) {
	// Hook runs carry only this description of what the hook does.
	status := "Tracking shell segments"
	if directory != "" {
		status = "Applying VCS guard"
	}
	input := map[string]any{"helper": helper, "directory": directory, "sudo_directory": sudoDirectory, "tracker": tracker,
		"hook_event_name": "${hook_event_name}", "tool_name": "${tool_name}", "tool_use_id": "${tool_use_id}",
		"tool_input": map[string]string{"command": "${tool_input.command}"}}
	identity := map[string]any{
		"event_name": "pre_tool_use", "matcher": "^Bash$",
		"hooks": []map[string]any{{"type": "mcp_tool", "server": HookServer, "tool": HookTool, "input": input, "timeout": 5, "statusMessage": status}},
	}
	data, err := json.Marshal(identity, json.Deterministic(true))
	if err != nil {
		return "", "", "", err
	}
	hash := sha256.Sum256(data)
	identityHash = "sha256:" + hex.EncodeToString(hash[:])
	config = fmt.Sprintf(`hooks.PreToolUse=[{matcher="^Bash$",hooks=[{type="mcp_tool",server=%q,tool=%q,input={helper=%q,directory=%q,sudo_directory=%q,tracker=%q,hook_event_name="${hook_event_name}",tool_name="${tool_name}",tool_use_id="${tool_use_id}",tool_input={command="${tool_input.command}"}},timeout=5,statusMessage=%q}]}]`, HookServer, HookTool, helper, directory, sudoDirectory, tracker, status)
	state = strconv.Quote(HookKey) + "={trusted_hash=" + strconv.Quote(identityHash) + "}"
	return config, state, identityHash, nil
}

// RewriteHook only instruments the command. Approval occurs in the shell's helper
// when the command is actually reached, never for an unexecuted branch.
func RewriteHook(request HookInput) (any, error) {
	if request.Event != "PreToolUse" || request.Tool != "Bash" || request.Input.Command == nil {
		return nil, fmt.Errorf("unexpected hook input")
	}
	if !filepath.IsAbs(request.Helper) {
		return nil, fmt.Errorf("hook helper must be absolute")
	}
	for _, path := range []string{request.Directory, request.SudoDirectory, request.Tracker} {
		if path != "" && !filepath.IsAbs(path) {
			return nil, fmt.Errorf("hook resource must be absolute")
		}
	}
	changed := *request.Input.Command
	if request.Directory != "" || request.SudoDirectory != "" {
		var err error
		changed, err = RewriteCommands(changed, request.Helper, request.Directory, request.SudoDirectory, request.Item)
		if err != nil {
			return nil, err
		}
	}
	if tracker := request.Tracker; tracker != "" {
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
	return response, nil
}
