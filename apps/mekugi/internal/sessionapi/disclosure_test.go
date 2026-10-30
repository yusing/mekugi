package sessionapi

import (
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/contracts/observation"
	activityfeed "github.com/yusing/mekugi/modules/activity/feed"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
)

func TestProjectionPreservesHiddenOutputAndPendingSource(t *testing.T) {
	for _, full := range []string{"complete…", strings.Repeat("界", activityfeed.SummaryLineBytes) + "hidden end"} {
		native := sessionpresentation.ActivityEntry{Kind: "tool", Native: &sessionpresentation.NativeItem{}}
		sessionpresentation.CommandOutput(&native, observation.Item{Type: "commandExecution", AggregatedOutput: &full}, native.Observed)
		command := sessionpresentation.CommandPresentation(native)
		command.Script, command.Display = "printf output", "printf output"
		item := activity.ItemContent{Kind: "tool", Text: "Run `printf output`", Command: command, Details: &activity.ItemDetails{Output: 1}}
		values := projectValues(item.Values(), false)
		if len(values) != 1 || values[0].TailOmitted != 0 || values[0].TailClipped != (len(full) > activityfeed.SummaryLineBytes) || (values[0].DetailsAvailable != nil) != values[0].TailClipped {
			t.Fatalf("output omission facts lost: %+v", values)
		}
	}
	for _, tc := range []struct {
		source, code, language string
		omitted                bool
	}{
		{"printf ready\n", "printf ready", "bash", false},
		{"printf ready\nprintf hidden", "printf ready", "bash", true},
		{"python3 - <<'PY'\nprint('inner program body')\nPY", "print('inner program body')", "python", false},
	} {
		source := tc.source
		command := activity.Command{Source: 7, Script: source, Display: source, Segments: []activity.CommandSegment{{Source: source, Pending: true}}}
		item := activity.ItemContent{Seq: 7, Kind: "tool", Command: command, Details: &activity.ItemDetails{CommandText: source}}
		values := projectValues(item.Values(), false)
		if len(values) != 1 || values[0].Code != tc.code || values[0].Lang != tc.language || values[0].SourceOmitted != tc.omitted || (values[0].DetailsAvailable != nil) != tc.omitted {
			t.Fatalf("pending source omission facts lost: %+v", values)
		}
		if pages := command.DetailValues(); values[0].SourceOmitted && (len(pages) != 1 || pages[0].Code != source) {
			t.Fatalf("original source not retained: %+v", pages)
		}
	}
}

func TestProjectionMarksQuestionChoicesOmittedInline(t *testing.T) {
	for _, options := range []string{"", "Readers · Developers"} {
		item := activity.ItemContent{Kind: "question", Details: &activity.ItemDetails{Questions: []activity.Question{{Text: "Which audience?", Options: options}}}}
		values := projectValues(item.Values(), false)
		if len(values) != 1 || values[0].SourceOmitted != (options != "") || (values[0].DetailsAvailable != nil) != (options != "") {
			t.Fatalf("question omission facts lost: %+v", values)
		}
	}
}

func TestDetailsFactSurvivesProjectionAndCompaction(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value activity.Value
		want  bool
	}{
		{"inline command and output", activity.Value{Kind: "op", Code: "echo ok", Tail: []string{"ok"}}, false},
		{"identical tool evidence", activity.Value{Kind: "op", Code: "{}", DetailCode: "{}", Fenced: true}, false},
		{"original tool evidence", activity.Value{Kind: "op", DetailCode: "original arguments"}, true},
		{"hidden body", activity.Value{Kind: "op", Body: strings.Repeat("details", 100)}, true},
		{"progress envelope", activity.Value{Kind: "progress", Body: "Interrupt requested", DetailCode: "original arguments"}, false},
		{"authored response", activity.Value{Kind: "final", Body: "Complete response"}, false},
		{"reasoning disclosure", activity.Value{Kind: "summary", Body: "Expanded thought"}, false},
		{"missing compacted context", activity.ItemContent{Kind: "compaction"}.Values()[0], true},
		{"successful hook output", activity.Value{Kind: "op", Hook: &activity.HookDetails{Status: "completed"}, Tail: []string{"hidden output"}}, true},
		{"failed hook output inline", activity.Value{Kind: "op", Hook: &activity.HookDetails{Status: "failed"}, Tail: []string{"visible output"}}, false},
		{"blank output", activity.Value{Kind: "op", Tail: []string{"", " "}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			full := projectEntry(activity.Entry{Values: []activity.Value{tc.value}}, false, "/root")
			for _, value := range []entry{full, compactEntry(full)} {
				if got := value.Values[0].DetailsAvailable != nil; got != tc.want {
					t.Fatalf("disclosure=%v, want %v: %+v", got, tc.want, value.Values[0])
				}
			}
		})
	}
	item := activity.ItemContent{Kind: "error", Agent: "Main", Text: "Complete failure"}
	full := projectEntry(activity.Entry{Agent: "Main", Kind: "error", Values: item.Values()}, false, "/root/worker")
	if disclosure := full.Values[0].DetailsAvailable; disclosure == nil || disclosure.Owner != "/root/worker" {
		t.Fatalf("local diagnostic owner lost: %+v", disclosure)
	}
}
