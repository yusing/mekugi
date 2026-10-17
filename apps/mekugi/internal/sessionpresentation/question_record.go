package sessionpresentation

import (
	"fmt"
	"slices"
	"strings"
	"time"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/conversation/prompt"
)

// RecordQuestion projects the admitted Conversation call without response handles.
// Source: apps/mekugi/internal/router/app_server_questions.go:455:503@4b40d245.
func RecordQuestion(c *prompt.Call, request, selected string, at time.Time, sources ...*ActivitySource) {
	states := []string{}
	var body []string
	var records []activitypresentation.Question
	for _, q := range c.View().Questions {
		state := q.State
		if !slices.Contains(states, state) {
			states = append(states, state)
		}
		record := activitypresentation.Question{Text: q.Text, State: state}
		line := q.Text
		if len(q.Answer) > 0 {
			answers := q.Answer
			record.Answer = strings.Join(answers, "\n\n")
			line += "\n└ " + record.Answer
		} else if q.Pending && len(q.Choices) > 0 {
			labels := []string{}
			for _, o := range q.Choices {
				labels = append(labels, o.Label)
			}
			record.Options = strings.Join(labels, " · ")
			line += "\n" + record.Options
		}
		body = append(body, line)
		records = append(records, record)
	}
	label := fmt.Sprintf("%d questions · %s", len(c.Questions), strings.Join(states, ", "))
	if len(c.Questions) == 1 {
		label = strings.Replace(label, "questions", "question", 1)
	}
	if len(request) == 0 && !c.Resolved && selected == "" {
		label += " · answer starts a new turn"
	}
	text := label + "\n" + strings.Join(body, "\n\n")
	id := c.Item
	if len(request) > 0 {
		id = "question/" + request
	}
	for _, v := range sources {
		if v == nil {
			continue
		}
		entry := ActivityEntry{Seq: v.LastSeq + 1, Agent: "Main", Kind: "question", Text: text, Observed: at, Native: &NativeItem{Thread: c.Thread, Turn: c.Turn, Item: id, Phase: "question", Questions: records}}
		v.Apply(ActivityEvent{Kind: "entries", Entries: []ActivityEntry{entry}})
	}
}
