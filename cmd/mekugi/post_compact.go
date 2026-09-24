package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

const (
	postCompactMatcher       = "^compact$"
	postCompactTimeout       = 5
	postCompactContextLimit  = 5000
	postCompactStatusMessage = "Restoring Mekugi context"
	// Codex keys a CLI-layer hook by its synthetic source path, event, and indexes.
	// Explicit CLI hooks suppress registration, so this session hook is always 0:0.
	postCompactHookKey = "/<session-flags>/config.toml:session_start:0:0"
)

// Native discovery loads each configuration layer's hooks, so this session
// layer adds to user/project hooks. Explicit hooks in the same CLI layer win:
// don't overwrite an existing array or modify any caller configuration files.
func postCompactHookArgs(args []string, executable string) ([]string, bool) {
	index := slices.Index(args, "--")
	if index < 0 {
		index = len(args)
	}
	for i := 0; i < index; i++ {
		argument := args[i]
		var setting string
		switch {
		case argument == "-c" || argument == "--config":
			if i+1 < index {
				i++
				setting = args[i]
			}
		case strings.HasPrefix(argument, "--config="):
			setting = strings.TrimPrefix(argument, "--config=")
		case strings.HasPrefix(argument, "-c="):
			setting = strings.TrimPrefix(argument, "-c=")
		case strings.HasPrefix(argument, "-c"):
			setting = strings.TrimPrefix(argument, "-c")
		}
		key, _, _ := strings.Cut(setting, "=")
		key = strings.TrimSpace(key)
		if key == "hooks" || strings.HasPrefix(key, "hooks.") {
			return args, false
		}
	}
	command := "'" + strings.ReplaceAll(executable, "'", "'\\''") + "' post-compact"
	hook := fmt.Sprintf(`hooks.SessionStart=[{matcher=%q,hooks=[{type="command",command=%q,timeout=%d,additionalContextLimit=%d,statusMessage=%q}]}]`,
		postCompactMatcher, command, postCompactTimeout, postCompactContextLimit, postCompactStatusMessage)
	// Trust only this exact session hook. The key contains dots, which Codex's -c
	// path parser would split, so it is quoted inside an inline table. A stale
	// hash leaves the hook "modified" under Codex's normal review, and a user's
	// enabled=false state still wins because Codex merges state fields per layer.
	settings := []string{"-c", hook}
	if hash, err := postCompactTrustHash(command); err == nil {
		settings = append(settings, "-c", fmt.Sprintf(`hooks.state={%s={trusted_hash=%s}}`, strconv.Quote(postCompactHookKey), strconv.Quote(hash)))
	}
	return slices.Insert(slices.Clone(args), index, settings...), true
}

// postCompactTrustHash mirrors Codex's hook_hash: SHA-256 over the canonical
// (key-sorted, compact) JSON of the normalized hook identity. Unset optional
// fields are omitted, and the context limit is kept because it differs from
// Codex's 2500-token default. Encoding fails only for invalid UTF-8, which
// Codex cannot hold either; the hook then stays under normal review.
func postCompactTrustHash(command string) (string, error) {
	identity := map[string]any{
		"event_name": "session_start",
		"matcher":    postCompactMatcher,
		"hooks": []map[string]any{{
			"type":                   "command",
			"command":                command,
			"timeout":                postCompactTimeout,
			"async":                  false,
			"statusMessage":          postCompactStatusMessage,
			"additionalContextLimit": postCompactContextLimit,
		}},
	}
	encoded, err := json.Marshal(identity, json.Deterministic(true))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
