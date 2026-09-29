package router

import (
	"runtime/debug"
	"strings"

	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

// Read the running executable's build identity, never the workspace checkout.
func mekugiVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" && setting.Value != "" {
				return setting.Value[:min(8, len(setting.Value))]
			}
		}
		if info.Main.Version != "" && info.Main.Version != "(devel)" {
			return info.Main.Version
		}
	}
	return "dev"
}

// Initialize's first user-agent token identifies the running app-server build.
func backendVersion(agent string) string {
	token, _, _ := strings.Cut(agent, " ")
	_, version, ok := strings.Cut(token, "/")
	if !ok {
		return ""
	}
	for _, r := range version {
		if r != '.' && r != '-' && r != '+' && (r < '0' || r > '9') && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return ""
		}
	}
	return strings.TrimPrefix(version, "v")
}

func (u *appServerUI) welcome() string {
	label := "Mekugi " + mekugiVersion() + " • codex"
	if u.backendVersion != "" {
		label += " v" + u.backendVersion
	}
	return "\x1b[1m" + label + activityui.Reset
}
