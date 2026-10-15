// Source: internal/vcsguard/hook.go:18:106@[2ba2151a] native hook protocol.
package shellhook

import (
	"crypto/sha256"
	"encoding/hex"
	json "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/yusing/mekugi/libraries/shellinstrumentation"

	"github.com/yusing/mekugi/libraries/commandanalysis/shellsyntax"
)

const Key = "/<session-flags>/config.toml:pre_tool_use:0:0"

const Environment = "MEKUGI_VCS_GUARD_HOOK"

const Flag = "--guard-hook"

// Input binds session resources in the trusted hook configuration. The
// remaining fields are expanded from Codex's native hook event, not router cwd.
type Input struct {
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

// Config uses the native session-layer hook and exact trust identity. It
// neither changes user files nor grants sandbox permissions.
func Config(helper, directory, sudoDirectory, tracker string) (config, state, identityHash string, err error) {
	// Hook runs carry only this description of what the hook does.
	status := "Tracking shell segments"
	if directory != "" {
		status = "Applying VCS guard"
	}
	arguments := []string{helper, Flag, directory, sudoDirectory, tracker}
	for i := range arguments {
		arguments[i] = shellsyntax.Quote(arguments[i])
	}
	// The host may continue after an ordinary nonzero hook exit. A surviving
	// adapter translates helper launch/process failure into its blocking form.
	// Use sh explicitly so the selected hook shell need not parse POSIX control flow.
	adapter := "if " + strings.Join(arguments, " ") + "; then :; else printf '%s\\n' 'mekugi: approval guard hook failed; command blocked' >&2; exit 2; fi"
	command := "/bin/sh -c " + shellsyntax.Quote(adapter)
	identity := map[string]any{
		"event_name": "pre_tool_use", "matcher": "^Bash$",
		"hooks": []map[string]any{{"type": "command", "command": command, "timeout": 5, "async": false, "statusMessage": status}},
	}
	data, err := json.Marshal(identity, json.Deterministic(true))
	if err != nil {
		return "", "", "", err
	}
	hash := sha256.Sum256(data)
	identityHash = "sha256:" + hex.EncodeToString(hash[:])
	config = fmt.Sprintf(`hooks.PreToolUse=[{matcher="^Bash$",hooks=[{type="command",command=%q,timeout=5,async=false,statusMessage=%q}]}]`, command, status)
	state = strconv.Quote(Key) + "={trusted_hash=" + strconv.Quote(identityHash) + "}"
	return config, state, identityHash, nil
}

// Rewrite only instruments the command. Approval occurs in the shell's helper
// when the command is actually reached, never for an unexecuted branch.
func Rewrite(request Input) (any, error) {
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
		changed, err = shellinstrumentation.RewriteCommands(changed, request.Helper, request.Directory, request.SudoDirectory, request.Item)
		if err != nil {
			return nil, err
		}
	}
	if tracker := request.Tracker; tracker != "" {
		if info, err := os.Stat(tracker); err == nil && info.Mode().IsRegular() {
			changed = shellinstrumentation.ShScript(tracker, changed)
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
