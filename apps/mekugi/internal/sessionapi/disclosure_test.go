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
		if len(values) != 1 || values[0].TailOmitted != 0 || values[0].TailClipped != (len(full) > activityfeed.SummaryLineBytes) {
			t.Fatalf("output omission facts lost: %+v", values)
		}
	}
	for _, source := range []string{"printf ready\n", "python3 - <<'PY'\nprint('hidden program body')\nPY"} {
		command := activity.Command{Source: 7, Script: source, Display: source, Segments: []activity.CommandSegment{{Source: source, Pending: true}}}
		item := activity.ItemContent{Seq: 7, Kind: "tool", Command: command, Details: &activity.ItemDetails{CommandText: source}}
		values := projectValues(item.Values(), false)
		if len(values) != 1 || values[0].SourceOmitted != strings.Contains(source, "hidden program") {
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
		if len(values) != 1 || values[0].SourceOmitted != (options != "") {
			t.Fatalf("question omission facts lost: %+v", values)
		}
	}
}
