package router

import "strings"

const maxTokensArgumentError = "--max-tokens requires one integer from 1 to 15500 and cannot repeat"

// Expand only the next option, never operands owned by a command or -- boundary.
func expandMaxTokensOption(arguments []string) []string {
	if len(arguments) != 0 {
		if value, ok := strings.CutPrefix(arguments[0], "--max-tokens="); ok {
			return append([]string{"--max-tokens", value}, arguments[1:]...)
		}
	}
	return arguments
}
