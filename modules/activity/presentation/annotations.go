package presentation

// RetainAnnotations keeps source-output notes across a fresh item projection.
// Outcomes, approval and captured operations come from the current source instead.
// Source: apps/mekugi/internal/router/live_activity_app_server.go:177:184@1bcbdd5a.
func RetainAnnotations(values, current []Value) []Value {
	var repeated string
	var filters []Value
	for _, annotation := range current {
		if annotation.Repeated != "" {
			repeated = annotation.Repeated
		}
		if annotation.Kind == "filter" {
			annotation.Repeated = ""
			filters = append(filters, annotation)
		}
	}
	if len(values) > 0 && repeated != "" {
		values[len(values)-1].Repeated = repeated
	}
	return append(values, CloneValues(filters)...)
}
