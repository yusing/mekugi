// Source: internal/router/app_server_guard_hook.go:64:137@[2ba2151a] effective-hook validation.
package shellhook

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

func Validate(data []byte, cwd, hash string) error {
	hooks, err := catalog(data, cwd)
	if err != nil {
		return err
	}
	found := false
	for _, hook := range hooks {
		matches, err := hook.shellConflict()
		if err != nil {
			return err
		}
		if !matches {
			continue
		}
		// The host hash authenticates the complete generated configuration and
		// resource paths. Its array index is not part of that identity.
		if hook.Handler == "command" && hook.Hash == hash && hook.Matcher == "^Bash$" {
			found = true
			continue
		}
		return fmt.Errorf("shell PreToolUse hook %q conflicts with the shell guard; command rewrites do not compose", hook.Key)
	}
	if !found {
		return fmt.Errorf("the shell guard hook is missing, disabled, or untrusted")
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
		for name := range strings.SplitSeq(pattern, "|") {
			if slices.Contains(names, name) {
				return true, nil
			}
		}
		return false, nil
	}
	// Rust regex accepts class algebra and extended word assertions that Go
	// can silently interpret as literal text. Reject those before using Go to
	// classify the fixed ASCII tool names; unsupported Go syntax also fails.
	classStart := -1
	for i := 0; i < len(pattern); i++ {
		if pattern[i] == '\\' && i+1 < len(pattern) {
			next := pattern[i+1]
			if classStart < 0 && (next == '<' || next == '>' || next == 'b' && i+2 < len(pattern) && pattern[i+2] == '{') {
				return false, fmt.Errorf("unsupported Rust word assertion")
			}
			i++
			continue
		}
		switch pattern[i] {
		case '[':
			if classStart >= 0 {
				// POSIX atoms are shared syntax, not nested Rust classes.
				if strings.HasPrefix(pattern[i:], "[:") {
					if end := strings.Index(pattern[i+2:], ":]"); end >= 0 {
						i += end + 3
						continue
					}
				}
				return false, fmt.Errorf("unsupported Rust nested character class")
			}
			classStart = i
		case ']':
			if classStart >= 0 && i != classStart+1 && !(i == classStart+2 && pattern[classStart+1] == '^') {
				classStart = -1
			}
		default:
			if classStart >= 0 && i+1 < len(pattern) && (pattern[i:i+2] == "&&" || pattern[i:i+2] == "--" || pattern[i:i+2] == "~~") {
				return false, fmt.Errorf("unsupported Rust character-class operation")
			}
		}
	}
	matcher, err := regexp.Compile(pattern)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(names, matcher.MatchString), nil
}
