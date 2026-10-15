// Source: internal/router/app_server_guard_hook.go:64:137@[2ba2151a] effective-hook validation.
package shellhook

import (
	json "encoding/json/v2"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

func Validate(data []byte, cwd, hash string) error {
	var result struct {
		Data []struct {
			Cwd    string `json:"cwd"`
			Errors []any  `json:"errors"`
			Hooks  []struct {
				Key     string `json:"key"`
				Event   string `json:"eventName"`
				Handler string `json:"handlerType"`
				Hash    string `json:"currentHash"`
				Matcher string `json:"matcher"`
				Trust   string `json:"trustStatus"`
				Enabled bool   `json:"enabled"`
				Async   bool   `json:"async"`
			} `json:"hooks"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return fmt.Errorf("read approval hook: %w", err)
	}
	found := false
	for _, entry := range result.Data {
		if entry.Cwd != cwd || len(entry.Errors) != 0 {
			return fmt.Errorf("cannot verify hooks for the selected workspace")
		}
		for _, hook := range entry.Hooks {
			if hook.Event != "preToolUse" || !hook.Enabled || (hook.Trust != "trusted" && hook.Trust != "managed") {
				continue
			}
			if hook.Key == Key && hook.Handler == "command" && hook.Hash == hash && hook.Matcher == "^Bash$" && !hook.Async {
				found = true
				continue
			}
			if hook.Async {
				continue
			}
			matches, err := matchesShell(hook.Matcher)
			if err != nil {
				return fmt.Errorf("cannot verify hook matcher %q", hook.Matcher)
			}
			if matches {
				return fmt.Errorf("shell PreToolUse hook %q conflicts with the VCS guard; command rewrites do not compose", hook.Key)
			}
		}
	}
	if !found {
		return fmt.Errorf("the VCS guard hook is missing, disabled, or untrusted")
	}
	return nil
}

// Source: codex-rs/hooks/src/engine/matcher.rs HookMatcher @7135b303d.
// Plain names are exact alternatives; only other patterns are regular expressions.
func matchesShell(pattern string) (bool, error) {
	if pattern == "" || pattern == "*" {
		return true, nil
	}
	names := []string{"Bash", "exec_command", "shell", "shell_command"}
	if strings.IndexFunc(pattern, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '|')
	}) < 0 {
		for _, name := range strings.Split(pattern, "|") {
			if slices.Contains(names, name) {
				return true, nil
			}
		}
		return false, nil
	}
	matcher, err := regexp.Compile(pattern)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(names, matcher.MatchString), nil
}
