// Package sudoask owns invocation-local sudo askpass setup.
package sudoask

import (
	"path/filepath"
	"strings"
)

const (
	Directory            = "sudo-guard"
	Askpass              = "mekugi-sudo-askpass"
	DirectoryEnvironment = "MEKUGI_SUDO_DIRECTORY"
	CommandEnvironment   = "MEKUGI_SUDO_COMMAND"
)

func Path(frontend string) string { return filepath.Join(filepath.Dir(frontend), Directory) }

// Interactive leaves explicitly selected input modes to sudo.
func Interactive(args []string) bool {
	return applicable(args, true)
}

// CommandApproval excludes listing and informational or timestamp-only modes,
// independently of sudo's authentication input policy.
func CommandApproval(args []string) bool {
	return applicable(args, false)
}

// Parse options only: a target command's flags must not affect sudo policy.
func applicable(args []string, password bool) bool {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.Contains(arg, "=") && !strings.HasPrefix(arg, "-") {
			continue
		}
		if arg == "--" || !strings.HasPrefix(arg, "-") || arg == "-" {
			break
		}
		if strings.HasPrefix(arg, "--") {
			name, _, attached := strings.Cut(arg, "=")
			prefix := name
			// sudo accepts unique long-option abbreviations. An ambiguous or
			// invalid prefix is left unchanged for sudo to diagnose.
			matches := 0
			for _, option := range []string{"auth-type", "login-class", "askpass", "background", "bell", "close-from", "chdir", "preserve-env", "edit", "group", "set-home", "help", "host", "login", "remove-timestamp", "reset-timestamp", "list", "non-interactive", "no-update", "preserve-groups", "prompt", "chroot", "role", "stdin", "shell", "type", "command-timeout", "other-user", "user", "version", "validate"} {
				if strings.HasPrefix("--"+option, prefix) {
					matches++
					name = "--" + option
				}
			}
			if matches != 1 {
				return false
			}
			switch name {
			case "--list", "--help", "--version", "--remove-timestamp":
				return false
			case "--non-interactive", "--stdin", "--askpass":
				if password {
					return false
				}
			case "--auth-type", "--login-class", "--close-from", "--chdir", "--group", "--host", "--prompt", "--chroot", "--role", "--type", "--command-timeout", "--other-user", "--user":
				if !attached {
					i++
				}
			}
			continue
		}
		for j := 1; j < len(arg); j++ {
			switch arg[j] {
			case 'l', 'V', 'K':
				return false
			case 'n', 'S', 'A':
				if password {
					return false
				}
			case 'a', 'c', 'C', 'D', 'g', 'h', 'p', 'R', 'r', 't', 'T', 'U', 'u':
				if j == len(arg)-1 {
					i++
				}
				j = len(arg)
			}
		}
	}
	return len(args) > 0 && !(len(args) == 1 && (args[0] == "-k" || args[0] == "-h"))
}
