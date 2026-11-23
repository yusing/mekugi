// Source: internal/vcsguard/hook.go:18:106@[2ba2151a] native hook protocol.
package shellhook

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yusing/mekugi/libraries/commandargs"

	"github.com/yusing/mekugi/libraries/shellinstrumentation"
)

const Flag = "--tracking-hook"

// Input is the native hook event. Trusted tracking resources come from argv.
type Input struct {
	Event  string `json:"hook_event_name"`
	Tool   string `json:"tool_name"`
	Item   string `json:"tool_use_id"`
	Thread string `json:"session_id"`
	Turn   string `json:"turn_id"`
	CWD    string `json:"cwd"`
	Input  struct {
		Command *string `json:"command"`
	} `json:"tool_input"`
}

// Config uses the native session-layer hook and exact trust identity. It
// neither changes user files nor grants sandbox permissions.
func Config(helper, tracker, capture string, prior Invocation) (config, state, identityHash string, err error) {
	arguments := []string{helper, Flag, tracker}
	// The native session layer runs this observer after ordinary Bash hooks
	// so their effective input, including a replacement, is the source tracked.
	matcher := "Bash|MekugiCommandTracking"
	if capture != "" {
		arguments = append(arguments, capture)
	}
	for i := range arguments {
		arguments[i] = commandargs.Quote(arguments[i])
	}
	// Tracking is auxiliary, including when the helper is unavailable.
	command := "/bin/sh -c " + commandargs.Quote(strings.Join(arguments, " ")+" || :")
	identity := map[string]any{
		"event_name": "pre_tool_use", "matcher": matcher,
		"hooks": []map[string]any{{"type": "command", "command": command, "timeout": 5, "async": false, "statusMessage": "Tracking shell segments"}},
	}
	data, err := json.Marshal(identity, json.Deterministic(true))
	if err != nil {
		return "", "", "", err
	}
	hash := sha256.Sum256(data)
	identityHash = "sha256:" + hex.EncodeToString(hash[:])
	group, err := json.Marshal(map[string]any{"matcher": matcher, "hooks": identity["hooks"]})
	if err != nil {
		return "", "", "", err
	}
	groups := append(slices.Clone(prior.PreToolUse), group)
	states := maps.Clone(prior.State)
	if states == nil {
		states = make(map[string]jsontext.Value)
	}
	key := fmt.Sprintf("/<session-flags>/config.toml:pre_tool_use:%d:0", len(prior.PreToolUse))
	if _, exists := states[key]; exists {
		return "", "", "", fmt.Errorf("explicit CLI state for tracking hook %q conflicts", key)
	}
	states[key], err = json.Marshal(map[string]any{"trusted_hash": identityHash, "enabled": true})
	if err != nil {
		return "", "", "", err
	}
	if capture != "" {
		// Patch observation has no approval role. Even helper launch failure
		// must leave native patch execution available.
		auxiliary := map[string]any{
			"event_name": "pre_tool_use", "matcher": "^apply_patch$",
			"hooks": []map[string]any{{"type": "command", "command": command, "timeout": 5, "async": false, "statusMessage": "Observing patch targets"}},
		}
		data, err := json.Marshal(auxiliary, json.Deterministic(true))
		if err != nil {
			return "", "", "", err
		}
		hash := sha256.Sum256(data)
		key := fmt.Sprintf("/<session-flags>/config.toml:pre_tool_use:%d:0", len(groups))
		if _, exists := states[key]; exists {
			return "", "", "", fmt.Errorf("explicit CLI state for tracking hook %q conflicts", key)
		}
		states[key], err = json.Marshal(map[string]any{"trusted_hash": "sha256:" + hex.EncodeToString(hash[:]), "enabled": true})
		if err != nil {
			return "", "", "", err
		}
		group, err := json.Marshal(map[string]any{"matcher": auxiliary["matcher"], "hooks": auxiliary["hooks"]})
		if err != nil {
			return "", "", "", err
		}
		groups = append(groups, group)
	}
	groupJSON, err := json.Marshal(&groups)
	if err != nil {
		return "", "", "", err
	}
	stateJSON, err := json.Marshal(&states)
	if err != nil {
		return "", "", "", err
	}
	config, err = inlineTOML(groupJSON)
	if err != nil {
		return "", "", "", err
	}
	state, err = inlineTOML(stateJSON)
	if err != nil {
		return "", "", "", err
	}
	config, state = "hooks.PreToolUse="+config, "hooks.state="+state
	return config, state, identityHash, nil
}

// Rewrite instruments tracked sh commands and passes apply_patch through.
func Rewrite(request Input, tracker string) (any, error) {
	if request.Event != "PreToolUse" || (request.Tool != "Bash" && request.Tool != "apply_patch") || request.Input.Command == nil {
		return nil, fmt.Errorf("unexpected hook input")
	}
	if tracker != "" && !filepath.IsAbs(tracker) {
		return nil, fmt.Errorf("hook tracker must be absolute")
	}
	if request.Tool == "apply_patch" {
		return map[string]any{}, nil
	}
	changed := *request.Input.Command
	if tracker != "" {
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
