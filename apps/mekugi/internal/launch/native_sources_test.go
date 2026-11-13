package launch

import (
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/sessionreplay/playback"
)

func TestNativeSourceAcquisitionLiveRestoredAndReplay(t *testing.T) {
	for _, purpose := range []string{"preload", "reload"} {
		t.Run(purpose, func(t *testing.T) {
			directory := t.TempDir()
			live := newAppServerSessionTestUI(t, directory)
			live.clock = func() time.Time { return time.UnixMilli(replayTestEpoch) }
			receipt := appserver.SourceAcquisition{ID: "acquisition", Purpose: purpose, Sources: []appserver.SourceAcquisitionResult{
				{Reference: "@source.txt#L2", Kind: "file", Path: filepath.Join(directory, "source.txt"), Success: true},
				{Reference: "@missing.txt", Kind: "file", Path: filepath.Join(directory, "missing.txt"), Diagnostic: "permission denied"},
				{Reference: "$review", Kind: "skill", Name: "review", Success: true},
				{Reference: "$denied", Kind: "skill", Name: "denied", Diagnostic: "not found"},
				{Reference: "mchanges:amber1..amber3", Kind: "changes", Selection: []string{"amber1..amber3"}, Success: true},
				{Reference: "invalid reference", Kind: "selection", Diagnostic: "invalid source reference"},
			}}
			item := map[string]any{"type": "function_call_output", "name": "context.reload", "output": "OPAQUE_MODEL_BODY"}
			for range 2 {
				appServerTestNotify(t, live, "rawResponseItem/completed", map[string]any{"threadId": "main", "item": item, "sourceAcquisition": receipt})
			}
			record := replayTestRecord("response_item", replayTestEpoch, item)
			record["metadata"] = map[string]any{"source_acquisition": receipt}
			if purpose == "reload" {
				record = replayTestRecord("compacted", replayTestEpoch, map[string]any{"replacement_history": []map[string]any{{"type": "function_call_output", "name": "context.reload", "output": "OPAQUE_MODEL_BODY", "metadata": map[string]any{"source_acquisition": receipt}}}})
			}
			metadata := replayTestRecord("session_meta", replayTestEpoch, map[string]any{"id": "main", "cwd": directory})
			path := replayTestWrite(t, directory, "rollout-main.jsonl", metadata, record, record)
			restored := newAppServerSessionTestUI(t, directory)
			if err := restored.restoreContent(binding.ThreadInfo{ID: "main", Cwd: directory, Path: path}); err != nil {
				t.Fatal(err)
			}
			source, err := replayTestService(t, nil).Read(t.Context(), playback.ReadOptions{Path: path, HistoryDirectory: filepath.Join(directory, "absent-history")})
			if err != nil {
				t.Fatal(err)
			}
			if source.Items != 0 || len(source.Events) != 2 || len(source.Unsupported) != 0 {
				t.Fatalf("source acquisition became shell activity or disappeared: %+v", source)
			}
			p := newTestUIReplayPlayback(new(activitymodel.Service), t.Context(), source, 1)
			t.Cleanup(p.close)
			if err := p.advance(source.End.Sub(source.Start)); err != nil {
				t.Fatal(err)
			}
			var expected []activity.Value
			for _, feed := range []*sessionpresentation.ActivitySource{live.attached.source, restored.attached.source, p.ui.source} {
				if len(feed.Entries) != 1 {
					t.Fatalf("acquisition omitted or duplicated before first turn: %+v", feed.Entries)
				}
				entry := feed.Entries[0]
				values := entry.Values
				if entry.Source.Kind != "attachments" || entry.Source.Native.Item != "native:sources:acquisition" || len(values) != 6 {
					t.Fatalf("acquisition identity/source count lost: %+v", entry)
				}
				if values[0].Path != "source.txt" || values[0].Verb != "Attached" || values[0].ExitCode != 0 || values[1].Path != "missing.txt" || values[1].ExitCode != 1 || values[1].Label != "permission denied" || values[4].Verb != "Attached changes" || values[4].Label != "amber1..amber3" || values[5].Verb != "Attach failed" {
					t.Fatalf("independent source outcomes changed: %+v", values)
				}
				for _, value := range values {
					if !value.Exited || value.AutoReload != (purpose == "reload") || value.Running || value.Code != "" || value.Body != "" || len(value.Tail) != 0 {
						t.Fatalf("reader receipt became fake execution or leaked bodies: %+v", value)
					}
				}
				if loads := activity.SkillLoads(entry); !slices.Equal(loads, []string{"review"}) {
					t.Fatalf("partial failure suppressed successful skill or loaded failed skill: %v", loads)
				}
				groups := entry.Publish().Values
				if len(groups) != 1 || groups[0].Kind != "attachments" || !groups[0].FailedOutcome() || groups[0].Label != "2 files, 2 skills, 1 change, 1 selection" {
					t.Fatalf("acquisition grouping lost failures: %+v", groups)
				}
				if expected == nil {
					expected = values
				} else if !reflect.DeepEqual(values, expected) {
					t.Fatalf("restored/replay source facts differ: got=%+v live=%+v", values, expected)
				}
			}
			paint := ansi.Strip(replayPlaybackTestPaint(t, p, 120, 30))
			if !strings.Contains(paint, "source.txt") || !strings.Contains(paint, "permission denied") || strings.Contains(paint, "OPAQUE_MODEL_BODY") || strings.Contains(paint, "context.reload") {
				t.Fatalf("rendered source receipt lost outcome or leaked transport: %s", paint)
			}
		})
	}
}
