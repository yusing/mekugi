package presentation

// RetainAnnotations keeps source-output notes across a fresh item projection.
// Outcomes, approval and captured operations come from the current source instead.
// Source: apps/mekugi/internal/router/live_activity_app_server.go:177:184@1bcbdd5a.
func RetainAnnotations(values, current []Value) []Value {
	var filters []Value
	for _, annotation := range current {
		if annotation.Kind == "filter" {
			filters = append(filters, annotation)
		}
	}
	return append(values, CloneValues(filters)...)
}
