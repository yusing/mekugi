package sessionpresentation

// Source: apps/mekugi/internal/router/live_activity_skills.go:14:16@45fbe279
func ContextBoundary(entry ActivityEntry) bool {
	return entry.Kind == "progress" && entry.Text == "Context compacted"
}
