package sessionpresentation

// CompletedCompaction accepts a completed boundary, including older history
// without an item status. Explicit unsuccessful outcomes never claim success.
func CompletedCompaction(status string) bool {
	return status == "" || status == "completed"
}

// Source: apps/mekugi/internal/router/live_activity_skills.go:14:16@45fbe279
func ContextBoundary(entry ActivityEntry) bool {
	return entry.Kind == "compaction"
}
