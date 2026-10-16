package router

import (
	"path/filepath"
	"strings"

	"github.com/yusing/mekugi/libraries/shellinstrumentation"
)

func toolActivityFenced(language, input string) string {
	fence := "```"
	for strings.Contains(input, fence) {
		fence += "`"
	}
	trailer := "\n"
	if strings.HasSuffix(input, "\n") {
		trailer = ""
	}
	return fence + language + "\n" + input + trailer + fence
}

func toolActivityShell(script string) string {
	return toolActivityShellInDirectory(script, "")
}

func toolActivityShellInDirectory(script, cwd string) string {
	return toolActivityShellLanguage(toolActivityShellSource(script), "bash", cwd)
}

func toolActivityShellSource(script string) string {
	return shellinstrumentation.DisplayScript(shellinstrumentation.ShOriginal(script), func(directory string) bool {
		if !filepath.IsAbs(directory) || !shellinstrumentation.IsGuardDirectory(directory) {
			return false
		}
		_, ok := toolRegistryIDFromDirectory(filepath.Dir(directory))
		return ok
	})
}
