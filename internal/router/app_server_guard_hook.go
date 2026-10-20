package router

import (
	json "encoding/json/v2"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/yusing/mekugi/internal/appserver"
	"github.com/yusing/mekugi/internal/vcsguard"
)

// Check the host's effective hook configuration before each new user turn.
// Hook rewrites compete rather than compose, so another synchronous shell
// hook could replace the guard's instrumentation. Keep that conflict visible.
type approvalHookCheck struct {
	hash                       string
	pendingThread, readyThread string
}

func (u *appServerUI) checkGuardHook() (bool, error) {
	check := &u.guardHookCheck
	if check.hash == "" || u.turn != "" {
		return true, nil
	}
	if check.readyThread == u.thread {
		return true, nil
	}
	if check.pendingThread == "" {
		check.pendingThread = u.thread
		u.status = "Checking approval guard…"
		if _, err := u.requestAs("hooks/list", "approval/hooks", map[string]any{"cwds": []string{u.session.cwd}}); err != nil {
			check.pendingThread = ""
			return false, err
		}
	}
	return false, nil
}

func (u *appServerUI) guardHookResponse(message appserver.Message) error {
	thread := u.guardHookCheck.pendingThread
	u.guardHookCheck.pendingThread = ""
	if thread != u.thread {
		return u.flushInput()
	}
	var err error
	if message.Error != nil {
		err = fmt.Errorf("read approval hook: %s", message.Error.Message)
	} else {
		err = validateGuardHook(message.Result, u.session.cwd, u.guardHookCheck.hash)
	}
	if err != nil {
		u.restoreDrafts(append(u.unsent, u.queued...)...)
		u.unsent, u.queued = nil, nil
		u.status = "Ready"
		u.setNotice("VCS guard: "+err.Error(), true)
		return nil
	}
	u.guardHookCheck.readyThread = thread
	return u.flushInput()
}

func validateGuardHook(data []byte, cwd, hash string) error {
	var result struct {
		Data []struct {
			Cwd    string `json:"cwd"`
			Errors []any  `json:"errors"`
			Hooks  []struct {
				Key     string `json:"key"`
				Event   string `json:"eventName"`
				Handler string `json:"handlerType"`
				Server  string `json:"server"`
				Tool    string `json:"tool"`
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
			if hook.Key == vcsguard.HookKey && hook.Handler == "mcpTool" && hook.Server == vcsguard.HookServer && hook.Tool == vcsguard.HookTool && hook.Hash == hash && hook.Matcher == "^Bash$" && !hook.Async {
				found = true
				continue
			}
			if hook.Async {
				continue
			}
			matches, err := guardHookMatchesShell(hook.Matcher)
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
func guardHookMatchesShell(pattern string) (bool, error) {
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
