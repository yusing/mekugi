package sessionpresentation

import (
	"github.com/yusing/mekugi/libraries/sourceanalysis/markdownsyntax"

	"strings"
)

// Source: apps/mekugi/internal/router/native_activity_assignments.go:1:47@45fbe279

type ActivityAssignment struct {
	ID, From, To, Text string `json:"-"`
}

// ActivityMessage is a message one agent delivered to another. Its text is
// the payload alone; an opaque payload leaves it empty.
type ActivityMessage struct {
	From, To, Text string `json:"-"`
}

// ActivityStart names the model settings a child actually ran with.
type ActivityStart struct {
	Model, Effort, Tier string `json:"-"`
}

// Label shows the settings as code spans; any may be unknown.
func (s *ActivityStart) Label() string {
	var parts []string
	for _, value := range []string{s.Model, s.Effort, s.Tier} {
		if value != "" {
			parts = append(parts, markdownsyntax.QuoteCode(value))
		}
	}
	return strings.Join(parts, " ")
}
