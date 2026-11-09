package contextnotice

import (
	"fmt"
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
		{"main", 899, 0, false},
		{"main", 900, 0, true},
		{"main", 901, 0, false},
		{"main", 1000, 0, false},
		{"child", 500, 0, true},
		{"main", 500, 1, true},
		{"main", 900, 1, true},
		{"main", 950, 1, false},
	} {
		warning, marks := store.Project("session", test.thread, Sample{Tokens: test.tokens, Window: 1000, Compactions: test.compactions}, func(string) Mark { return Mark{Key: "boundary"} })
		if (warning.Text != "") != test.want {
			t.Fatalf("%+v: warning=%+v", test, warning)
		}
		if test.want && (warning.Reminder != "<system-reminder>\n"+warning.Text+"\n</system-reminder>" || marks[len(marks)-1].Text != warning.Reminder || !strings.Contains(warning.Text, "%")) {
			t.Fatalf("warning and annotation diverged: %+v, %v", warning, marks)
		}
		if test.want && strings.Contains(warning.Text, "You must compact now.") != (test.tokens >= 900) {
			t.Fatalf("urgent instruction at wrong milestone: %+v, %+v", test, warning)
		}
	}
	for _, sample := range []Sample{{Tokens: 900}, {Tokens: 900, Window: 1000}} {
		warning, _ := store.Project("session", "unavailable-boundary", sample, func(string) Mark { return Mark{Index: -1} })
		if warning.Text != "" {
			t.Fatalf("unadmitted milestone emitted: %+v", warning)
		}
	}
}

func TestUrgentMilestoneWhenUsageSkipsThreshold(t *testing.T) {
	for _, tokens := range []uint64{900, 950, 1000, 1100} {
		t.Run(fmt.Sprint(tokens), func(t *testing.T) {
			var store Store
			warning, marks := store.Project("session", "main", Sample{Tokens: tokens, Window: 1000}, func(string) Mark { return Mark{Key: "boundary"} })
			want := fmt.Sprintf("Mekugi context usage: %d%% (%d/1000 tokens). You must compact now.", min(tokens/10, 100), tokens)
			if warning.Text != want || len(marks) != 1 || marks[0].Text != warning.Reminder {
				t.Fatalf("skipped threshold: warning=%+v, marks=%v", warning, marks)
			}
			warning, marks = store.Project("session", "main", Sample{Tokens: tokens, Window: 1000}, func(string) Mark { return Mark{Key: "next-boundary"} })
			if warning.Text != "" || len(marks) != 1 {
				t.Fatalf("urgent milestone repeated: warning=%+v, marks=%v", warning, marks)
			}
		})
	}
}
