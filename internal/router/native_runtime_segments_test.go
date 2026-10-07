package router

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/execsegment"
	"github.com/yusing/mekugi/internal/session"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func savedRuntimeSegments(t *testing.T, background bool) (string, ObservationBinding, []commandSegment, func()) {
	t.Helper()
	u, track := runtimeSegmentUI(t)
	binding := ObservationBinding{Runtime: "claude", Session: u.thread, Workspace: u.session.cwd}
	directory := t.TempDir()
	service, _, closeService := observationIsolationService(t, directory, binding)
	if err := service.owner.bind(t.Context(), binding); err != nil {
		t.Fatal(err)
	}
	// Preserve the fixture tracker when attaching the storage owner.
	service.owner.execTrack = u.execTrack
	u.attachRuntimeObservation(service)
	result := session.Event{Kind: "tool_result", ID: "command", Text: "first\n", Failed: true}
	if background {
		result.Text, result.Failed = "native launch placeholder", false
		result.Output = &session.CommandOutput{TaskID: "background"}
	}
	runtimeEvidenceEvent(t, u, result)
	track.apply(execsegment.Message{Type: execsegment.End, Index: 1, Code: new(1)})
	track.apply(execsegment.Message{Type: execsegment.Done, Code: new(1)})
	track.ended = true
	if background {
		u.runtimeFinishCommand("command", true)
	}
	u.flushRuntimeCommandSegments()
	awaitCommandSegments(t, u)
	parts := u.view.entries[0].native.segments
	if len(parts) != 3 {
		t.Fatalf("terminal report missing: %+v", parts)
	}
	return directory, binding, parts, closeService
}

func restoreSavedRuntimeSegments(t *testing.T, directory string, binding ObservationBinding, source *ObservationBinding, input, output, caller string, failed bool) *appServerUI {
	t.Helper()
	service, _, _ := observationIsolationService(t, directory, binding)
	if err := service.owner.bind(t.Context(), binding); err != nil {
		t.Fatal(err)
	}
	if source != nil {
		if err := service.owner.retainForkHistory(t.Context(), binding, *source, nil); err != nil {
			t.Fatal(err)
		}
	}
	u, _ := runtimeTestUI(t)
	u.session.cwd, u.thread = binding.Workspace, binding.Session
	u.attachRuntimeObservation(service)
	runtimeEvidenceEvent(t, u, session.Event{Kind: "tool", ID: "history/command", Role: "Bash", Text: input, Caller: caller, Historical: true})
	runtimeEvidenceEvent(t, u, session.Event{Kind: "tool_result", ID: "history/command", Text: output, Caller: caller, Failed: failed, Historical: true})
	return u
}

const savedSegmentInput = `{"command":"printf 'first\\n'; false && printf 'never\\n'"}`

func TestNativeRuntimeSavedSegmentsRestartAndVisibleHistoryIsolation(t *testing.T) {
	for _, background := range []bool{false, true} {
		t.Run(fmt.Sprint(background), func(t *testing.T) {
			directory, binding, original, closeService := savedRuntimeSegments(t, background)
			closeService()
			output := "first\n"
			if background {
				output = "native launch placeholder"
			}
			// A selected fork inherits exact native tool IDs, not the old session.
			source := binding
			binding.Session = "fresh-fork"
			u := restoreSavedRuntimeSegments(t, directory, binding, &source, savedSegmentInput, output, "", !background)
			for _, view := range []*liveActivityView{u.view, u.agents} {
				entry := view.entries[0]
				parts := entry.native.segments
				if len(parts) != len(original) || entry.native.running || entry.native.status != "failed" || !entry.native.collapsed || u.execTrack != nil {
					t.Fatalf("saved evidence restored live state or lost failure: %+v", entry.native)
				}
				for i, part := range parts {
					before := original[i]
					if part.source != before.source || part.exit != before.exit || part.skipped != before.skipped || part.timing != before.timing || part.running {
						t.Fatalf("segment %d changed on restart: %+v", i, part)
					}
					if !part.skipped && (part.output == nil || !part.output.View().Done || strings.Join(part.output.View().Lines, "\n") != strings.Join(before.output.View().Lines, "\n")) {
						t.Fatalf("segment %d lost output", i)
					}
				}
			}
			runtimeParityClick(t, u, u.view, 120, func(b activityui.Block) bool { return b.Output == u.view.entries[0].native.segments[0].output })
			if u.shell.output == nil || !strings.Contains(runtimeFrame(t, u, 120, 32), "first") {
				t.Fatal("saved segment did not open the original output dialog")
			}
			opened := u.view.entries[0].native.segments[0].output
			runtimeEvidenceEvent(t, u, session.Event{Kind: "session", SessionID: binding.Session})
			if u.view.entries[0].native.segments[0].output != opened {
				t.Fatal("native init replaced an open saved segment dialog")
			}
		})
	}
	directory, binding, _, closeService := savedRuntimeSegments(t, false)
	closeService()
	for _, name := range []string{"workspace", "session", "command", "result", "caller", "failure"} {
		t.Run(name, func(t *testing.T) {
			selected := binding
			input, output, caller, failed := savedSegmentInput, "first\n", "", true
			switch name {
			case "workspace":
				selected.Workspace = t.TempDir()
			case "session":
				selected.Session = "unrelated-native-session"
			case "command":
				input = `{"command":"printf changed; false && printf never"}`
			case "result":
				output = "other native result"
			case "caller":
				caller = "other-native-spawner"
			case "failure":
				failed = false
			}
			u := restoreSavedRuntimeSegments(t, directory, selected, nil, input, output, caller, failed)
			if len(u.view.entries[0].native.segments) != 0 || strings.Join(u.view.entries[0].native.output.View().Lines, "\n") != strings.TrimSuffix(output, "\n") {
				t.Fatal("unrelated native history borrowed saved segments or lost its own aggregate")
			}
		})
	}
}

func TestNativeRuntimeSavedSegmentsStorageFailureVisible(t *testing.T) {
	u, track := runtimeSegmentUI(t)
	binding := ObservationBinding{Runtime: "claude", Session: u.thread, Workspace: u.session.cwd}
	directory := t.TempDir()
	service, _, _ := observationIsolationService(t, directory, binding)
	if err := service.owner.bind(t.Context(), binding); err != nil {
		t.Fatal(err)
	}
	service.owner.execTrack = u.execTrack
	u.attachRuntimeObservation(service)
	key := "claude/" + commandSegmentsID(binding.Session, "command")
	if err := os.Mkdir(filepath.Join(directory, replayRecordName(binding.Workspace, key, false)), 0700); err != nil {
		t.Fatal(err)
	}
	track.apply(execsegment.Message{Type: execsegment.End, Index: 1, Code: new(1)})
	track.apply(execsegment.Message{Type: execsegment.Done, Code: new(1)})
	track.ended = true
	runtimeEvidenceEvent(t, u, session.Event{Kind: "tool_result", ID: "command", Text: "first\n", Failed: true})
	u.flushRuntimeCommandSegments()
	if u.commandSegmentPending != 1 {
		t.Fatal("failure report was not pending at shutdown")
	}
	var fallback bytes.Buffer
	u.finishRuntimeCommandSegments(&fallback)
	if !strings.Contains(fallback.String(), "could not be retained") || u.commandSegmentPending != 0 {
		t.Fatal("shutdown hid a failed pending write")
	}
	notices := u.applyCriticalNotices()
	runtimeFrame(t, u, 120, 40)
	if notices == nil || len(u.view.entries[0].native.segments) != 3 || u.noticeDetails.w == 0 {
		t.Fatal("storage failure hid the coverage gap or discarded live segments")
	}
	u.composerNoticeMouse(0, u.noticeDetails.x, u.noticeDetails.y, false)
	if u.shell.output == nil {
		t.Fatal("storage failure details did not open")
	}
	drawOutputDialog(u.shell)
	if !strings.Contains(u.shell.output.laid.Text, "could not be retained") {
		t.Fatal("storage failure details lost the coverage gap")
	}
	u.shell.outputKey("q")
	runtimeFrame(t, u, 120, 40)
	notices.finish(true)
	fallback.Reset()
	u.finishRuntimeCommandSegments(&fallback)
	if fallback.Len() != 0 {
		t.Fatal("shutdown repeated an acknowledged notice")
	}
}

func TestNativeRuntimeSavedSegmentsWaitForTranscriptResult(t *testing.T) {
	u, track := runtimeSegmentUI(t)
	track.apply(execsegment.Message{Type: execsegment.End, Index: 1, Code: new(1)})
	track.apply(execsegment.Message{Type: execsegment.Done, Code: new(1)})
	track.ended = true
	u.runtimeCommandOutput(session.Event{Kind: "command_output", ID: "command", Failed: true,
		Output: &session.CommandOutput{TaskID: "native-task", Done: true}})
	u.flushRuntimeCommandSegments()
	if !u.execTrack.tracking([3]string{u.thread, "", "command"}) {
		t.Fatal("terminal output discarded a report before its transcript identity arrived")
	}
	runtimeEvidenceEvent(t, u, session.Event{Kind: "tool_result", ID: "command", Text: "first\n", Failed: true})
	u.flushRuntimeCommandSegments()
	if len(u.view.entries[0].native.segments) != 3 || u.execTrack.tracking([3]string{u.thread, "", "command"}) {
		t.Fatal("matching transcript result did not finish the saved-segment edge")
	}
}

func TestUISnapshotNativeRuntimeSavedCommandSegments(t *testing.T) {
	directory, binding, _, closeService := savedRuntimeSegments(t, false)
	closeService()
	for _, width := range []int{48, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			selected := binding
			selected.Session = fmt.Sprint("saved-snapshot-", width)
			u := restoreSavedRuntimeSegments(t, directory, selected, &binding, savedSegmentInput, "first\n", "", true)
			uisnapshot.Assert(t, filepath.Join("testdata", "snapshots", fmt.Sprintf("native-runtime-segments-saved-%d.txt", width)), runtimeFrame(t, u, width, 32))
		})
	}
}
