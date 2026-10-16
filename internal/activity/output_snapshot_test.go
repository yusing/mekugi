package activity_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/activity"
)

func TestOutputSnapshotSurvivesStreamFinishAndRelease(t *testing.T) {
	var output activity.Output
	output.Write("first\nprogress")
	stream := output.View()
	wantStream := activity.OutputView{Lines: []string{"first", "progress"}}
	assertOutputSnapshot(t, stream, wantStream)

	output.Write(" 50%")
	appended := output.View()
	wantAppended := activity.OutputView{Lines: []string{"first", "progress 50%"}}
	assertOutputSnapshot(t, appended, wantAppended)
	assertOutputSnapshot(t, stream, wantStream)

	output.Write("\rdone\n")
	redrawn := output.View()
	wantRedrawn := activity.OutputView{Lines: []string{"first", "done"}}
	assertOutputSnapshot(t, redrawn, wantRedrawn)
	assertOutputSnapshot(t, appended, wantAppended)

	output.Finish(new("authoritative\nresult"), new(7))
	settled := output.View()
	wantSettled := activity.OutputView{Lines: []string{"authoritative", "result"}, Done: true, Exited: true, Exit: 7}
	assertOutputSnapshot(t, settled, wantSettled)
	assertOutputSnapshot(t, redrawn, wantRedrawn)

	output.Release()
	assertOutputSnapshot(t, output.View(), activity.OutputView{Done: true, Released: true, Exited: true, Exit: 7})
	assertOutputSnapshot(t, stream, wantStream)
	assertOutputSnapshot(t, appended, wantAppended)
	assertOutputSnapshot(t, redrawn, wantRedrawn)
	assertOutputSnapshot(t, settled, wantSettled)
}

func TestOutputSnapshotSurvivesHeadTrim(t *testing.T) {
	var output activity.Output
	output.Write("first\nsecond\n")
	before := output.View()
	want := activity.OutputView{Lines: []string{"first", "second"}}
	assertOutputSnapshot(t, before, want)

	line := strings.Repeat("x", activity.OutputLineBytes)
	output.Write(strings.Repeat(line+"\n", activity.OutputBytes/activity.OutputLineBytes+1))
	if current := output.View(); current.Dropped < 2 || len(current.Lines) == 0 || current.Lines[0] != line {
		t.Fatalf("head trim did not replace original rows: dropped=%d lines=%d", current.Dropped, len(current.Lines))
	}
	assertOutputSnapshot(t, before, want)
}

func TestOutputSnapshotSurvivesSharedRetentionRelease(t *testing.T) {
	var retention activity.Retention
	oldest := retention.New()
	oldest.Write("retained result\n")
	oldest.Finish(nil, nil)
	before := oldest.View()
	want := activity.OutputView{Lines: []string{"retained result"}, Done: true}
	assertOutputSnapshot(t, before, want)

	payload := strings.Repeat(strings.Repeat("x", activity.OutputLineBytes)+"\n", activity.OutputBytes/activity.OutputLineBytes)
	for range activity.OutputRetainedBytes/activity.OutputBytes + 2 {
		newer := retention.New()
		newer.Write(payload)
		newer.Finish(nil, nil)
		if oldest.View().Released {
			break
		}
	}
	assertOutputSnapshot(t, oldest.View(), activity.OutputView{Done: true, Released: true})
	assertOutputSnapshot(t, before, want)
}

func assertOutputSnapshot(t *testing.T, got, want activity.OutputView) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot = %+v, want %+v", got, want)
	}
}
