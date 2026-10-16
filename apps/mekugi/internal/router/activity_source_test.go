package router

import activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

func toolActivityShell(source string) string {
	return activitypresentation.ShellSource(toolActivityShellSource(source), "")
}
func toolActivityFenced(language, source string) string {
	return activitypresentation.SourceFence(language, source)
}
