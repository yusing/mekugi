package main

import "strings"

// codexArgumentValue reads one selector without consuming unrelated arguments.
func codexArgumentValue(args []string, index *int, short, long string) (string, bool) {
	arg := args[*index]
	if arg == short || arg == long {
		if *index+1 == len(args) {
			return "", false
		}
		*index++
		return args[*index], true
	}
	if value, ok := strings.CutPrefix(arg, long+"="); ok {
		return value, true
	}
	if value, ok := strings.CutPrefix(arg, short); ok {
		return strings.TrimPrefix(value, "="), true
	}
	return "", false
}
