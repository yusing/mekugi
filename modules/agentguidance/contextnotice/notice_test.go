package contextnotice

import (
	"strings"
	"testing"
)

func TestMilestonesShareWarningAndReminder(t *testing.T) {
	var store Store
	for _, test := range []struct {
		thread              string
		tokens, compactions uint64
		want                bool
	}{
		{"main", 499, 0, false},
		{"main", 500, 0, true},
		{"main", 590, 0, false},
		{"main", 600, 0, true},
		{"main", 700, 0, true},
		{"main", 800, 0, true},
		{"main", 900, 0, true},
		{"main", 1000, 0, false},
		{"child", 500, 0, true},
		{"main", 500, 1, true},
	} {
		warning, marks := store.Project("session", test.thread, Sample{Tokens: test.tokens, Window: 1000, Compactions: test.compactions}, func(string) Mark { return Mark{Key: "boundary"} })
		if (warning.Text != "") != test.want {
			t.Fatalf("%+v: warning=%+v", test, warning)
		}
		if test.want && (warning.Reminder != "<system-reminder>\n"+warning.Text+"\n</system-reminder>" || marks[len(marks)-1].Text != warning.Reminder || !strings.Contains(warning.Text, "%")) {
			t.Fatalf("warning and annotation diverged: %+v, %v", warning, marks)
		}
	}
	for _, sample := range []Sample{{Tokens: 900}, {Tokens: 900, Window: 1000}} {
		warning, _ := store.Project("session", "unavailable-boundary", sample, func(string) Mark { return Mark{Index: -1} })
		if warning.Text != "" {
			t.Fatalf("unadmitted milestone emitted: %+v", warning)
		}
	}
}
