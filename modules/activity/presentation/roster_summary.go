package presentation

import (
	"fmt"
	"strings"

	"github.com/yusing/mekugi/modules/activity/feed"
)

// CompactRoster keeps each semantic block in the existing value shape, without
// retained source, output or diagnostic evidence. Callers redact secrets first.
func CompactRoster(entries []Entry, caller string) []Value {
	if len(entries) == 0 || entries[len(entries)-1].Kind == "final" {
		return nil
	}
	var out []Value
	for _, entry := range entries {
		for _, v := range entry.Values {
			if v.Kind == "filter" {
				continue
			}
			compact := Value{Kind: v.Kind, Verb: v.RowVerb(), Live: v.Live, Failed: v.FailedOutcome()}
			compact.From, compact.To = v.Endpoints(caller)
			switch {
			case v.Kind == "progress":
				compact.Verb, compact.Body, compact.Label = "", v.Body, v.Label
				compact.WaitTargets, compact.WaitTimeout, compact.WaitReason = v.WaitTargets, v.WaitTimeout, v.ProgressReason()
			case v.Kind == "summary":
				compact.Verb, compact.Label = "", v.ReasoningLabel()
				if compact.Label == "" {
					body := strings.TrimSpace(ReasoningSummaryBody(v.Body))
					if len(body) <= 80 && !strings.Contains(body, "\n") {
						compact.Label = body
					}
				}
				if n := len(out); n > 0 && out[n-1].Kind == "summary" {
					out = out[:n-1]
				}
				if compact.Label == "" {
					compact.Verb = "Thought"
					if v.Live {
						compact.Verb = "Thinking"
					}
				}
			case compact.From != "" || compact.To != "":
				compact.Verb = ""
				if v.Kind == "start" {
					compact.Verb, compact.Label = "Started", v.Label
				}
			case v.Kind == "text" || v.Kind == "message" || v.Kind == "user":
				compact.Verb = "Writing"
			case v.Kind == "reads":
				compact.Label = v.ReadSubject()
			case v.Kind == "op":
				compact.Label = v.Label
				if compact.Label == "" || feed.Annotates(compact.Label) {
					compact.Code, _, _ = strings.Cut(v.PreviewCode(), "\n")
					length := 0
					for offset := range compact.Code {
						if length == 256 {
							compact.Code = compact.Code[:offset]
							break
						}
						length++
					}
				}
				if v.VCS() {
					head, source, _ := VCSSource(v.Label)
					added, removed := ChangeTotals(v.Changes)
					if added != 0 || removed != 0 {
						head = strings.TrimSpace(fmt.Sprintf("%s +%d -%d", head, added, removed))
					}
					compact.Label = strings.TrimSpace(head + " · " + source)
				}
				if v.ExitCode != 0 {
					compact.Label += fmt.Sprintf(" (exit %d)", v.ExitCode)
				}
			case v.Kind == "error":
				compact.Verb, compact.Label, compact.Failed = "", ErrorPreview(v.Body), true
			default:
				compact.Label = v.Label
			}
			out = append(out, compact)
		}
	}
	return out
}
