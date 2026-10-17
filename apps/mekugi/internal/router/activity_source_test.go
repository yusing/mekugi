package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/frontends"
	"github.com/yusing/mekugi/libraries/sourceanalysis/markdownsyntax"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

func toolActivityShell(source string) string {
	return activitypresentation.ShellSource(frontends.ShellDisplaySource(source), "")
}
func toolActivityFenced(language, source string) string {
	return markdownsyntax.SourceFence(language, source)
}
