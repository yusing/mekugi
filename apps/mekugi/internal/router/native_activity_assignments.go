package router

import (
	"github.com/yusing/mekugi/libraries/sourceanalysis/markdownsyntax"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	"strings"
	"time"
)

type activityAssignment struct {
	id, from, to, text string
	created            time.Time
}

// activityMessage is a message one agent delivered to another. Its text is
// the payload alone; an opaque payload leaves it empty.
type activityMessage struct{ from, to, text string }

// activityStart names the model settings a child actually ran with.
type activityStart struct{ model, effort, tier string }

// label shows the settings as code spans; any may be unknown.
func (s *activityStart) label() string {
	var parts []string
	for _, value := range []string{s.model, s.effort, s.tier} {
		if value != "" {
			parts = append(parts, markdownsyntax.QuoteCode(value))
		}
	}
	return strings.Join(parts, " ")
}

func supplementAssignment(a *activityAssignment) *activitypresentation.SupplementAssignment {
	if a == nil {
		return nil
	}
	return &activitypresentation.SupplementAssignment{ID: a.id, From: a.from, To: a.to, Text: a.text, Created: a.created}
}
func supplementMessage(m *activityMessage) *activitypresentation.SupplementMessage {
	if m == nil {
		return nil
	}
	return &activitypresentation.SupplementMessage{From: m.from, To: m.to, Text: m.text}
}
