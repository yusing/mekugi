package sessionpresentation

import (
	"path/filepath"
	"strings"

	"github.com/yusing/mekugi/libraries/shellinstrumentation"
)

// Source: apps/mekugi/internal/router/app_server_events.go:677:709@45fbe279
// Hide only the host's literal shell command wrapper, never evaluate its words
// or discard surrounding operations. Execution and retained items stay intact.
// Source: codex-rs/shell-command/src/bash.rs:106:121@86be5320 extract_bash_command
// Source: codex-rs/shell-command/src/powershell.rs:43:73@86be5320 extract_powershell_command
// Unlike Codex's argv helper, preserve PowerShell's extra script arguments.
func DisplayCommand(command string) string {
	args, ok := shellinstrumentation.CommandArgs(command)
	if !ok {
		return command
	}
	name := filepath.Base(args[0])
	name = strings.TrimSuffix(name, filepath.Ext(name))
	switch name {
	case "bash", "sh":
		if len(args) == 3 && (args[1] == "-lc" || args[1] == "-c") {
			return args[2]
		}
	case "pwsh", "powershell":
		for i := 1; i+1 < len(args); i++ {
			switch strings.ToLower(args[i]) {
			case "-nologo", "-noprofile":
			case "-command", "-c":
				if i+2 == len(args) {
					return args[i+1]
				}
				return command
			default:
				return command
			}
		}
	}
	return command
}
