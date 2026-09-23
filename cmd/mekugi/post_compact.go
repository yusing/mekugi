package main

import (
	"fmt"
	"slices"
	"strings"
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
	setting := fmt.Sprintf(`hooks.SessionStart=[{matcher="^compact$",hooks=[{type="command",command=%q,timeout=5,additionalContextLimit=5000,statusMessage="Restoring Mekugi context"}]}]`, command)
	return slices.Insert(slices.Clone(args), index, "-c", setting), true
}
