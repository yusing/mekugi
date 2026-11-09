package presentation

import (
	"fmt"
	"strings"
)

// AttachmentEvents publishes one invocation with its original source parameters.
// The retained records stay source-addressable for output annotations and recovery.
func AttachmentEvents(values []Value) []Value {
	if len(values) == 0 {
		return values
	}
	for _, v := range values {
		if !strings.HasPrefix(v.Verb, "Attached") && v.Verb != "Attach failed" {
			return values
		}
	}
	group := Group(Value{Kind: "attachments", Verb: "Attach", Source: values[0].Source}, values)
	counts := make(map[string]int)
	for _, v := range values {
		kind := v.AttachmentKind
		if kind == "" {
			switch v.Verb {
			case "Attached skill":
				kind = "skill"
			case "Attached changes":
				kind = "change"
			case "Attached selection":
				kind = "selection"
			default:
				kind = "file"
			}
		}
		counts[kind]++
		group.Failed = group.Failed || v.FailedOutcome() || v.Verb == "Attach failed"
		group.Running = group.Running || v.Running
	}
	var labels []string
	for _, kind := range []string{"file", "skill", "change", "selection", "omission"} {
		if n := counts[kind]; n > 0 {
			if n != 1 {
				kind += "s"
			}
			labels = append(labels, fmt.Sprintf("%d %s", n, kind))
		}
	}
	group.Label = strings.Join(labels, ", ")
	return []Value{group}
}
