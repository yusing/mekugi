package launch

import (
	"fmt"
	"github.com/yusing/mekugi/modules/agentguidance/compact"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/observation"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
)

const completionTestTrailer = compact.CompactMetadataStart + `{"sources":[{"changes":["blue1..blue10"]}]}` + compact.CompactMetadataEnd

func completionTestDelta(t *testing.T, u *appServerUI, delta string) {
	t.Helper()
	appServerTestNotify(t, u, "item/agentMessage/delta", map[string]any{"threadId": "main", "turnId": "turn", "itemId": "answer", "delta": delta})
}

func completionTestFinish(t *testing.T, u *appServerUI, text string) {
	t.Helper()
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": map[string]any{"id": "answer", "type": "agentMessage", "text": text}})
}

func TestNativeCompletionMetadataRenderedDelivery(t *testing.T) {
	for _, prefix := range []string{"Completed answer\n", "Completed answer\n\n- ```\n  quoted\n  ```\n\n"} {
		t.Run(prefix, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			u.shell.Transcript.Conversation = true
			u.testActivity(u.shell.Transcript).testAudience()
			completionTestDelta(t, u, prefix)
			for i := range len(completionTestTrailer) {
				completionTestDelta(t, u, completionTestTrailer[i:i+1])
				for _, entry := range u.attached.source.Entries {
					if entry.Source.Kind == "text" && entry.Source.Text != strings.TrimSuffix(prefix, "\n") {
						t.Fatalf("partial protocol reached Activity at byte %d: %q", i, entry.Source.Text)
					}
				}
				frame := ansi.Strip(strings.Join(u.testActivity(u.shell.Transcript).RenderFeed(100, 60).Lines, "\n"))
				if !strings.Contains(frame, "Completed answer") || strings.Contains(frame, "<!") || strings.Contains(frame, "mekugi-") {
					t.Fatalf("partial protocol flashed at byte %d:\n%s", i, frame)
				}
			}
			completionTestFinish(t, u, prefix+completionTestTrailer)
			frame := ansi.Strip(strings.Join(u.testActivity(u.shell.Transcript).RenderFeed(100, 60).Lines, "\n"))
			if strings.Contains(frame, "mekugi-") {
				t.Fatalf("final protocol flashed:\n%s", frame)
			}
		})
	}
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.shell.Transcript.Conversation = true
	u.testActivity(u.shell.Transcript).testAudience()
	paint := func() string {
		return ansi.Strip(strings.Join(u.testActivity(u.shell.Transcript).RenderFeed(100, 60).Lines, "\n"))
	}
	// The same history projection serves initial restoration and older pages.
	turns := []binding.HistoryTurn{{ID: "saved", Status: "completed", Items: []binding.ThreadItem{{ID: "restored", Type: "agentMessage", Text: "Restored answer\n" + completionTestTrailer}}}}
	u.restoreHistory(turns)
	if frame := paint(); !strings.Contains(frame, "Restored answer") || strings.Contains(frame, "mekugi-reload-v1") {
		t.Fatalf("restored frame:\n%s", frame)
	}
	if !strings.Contains(turns[0].Items[0].Text, completionTestTrailer) {
		t.Fatal("presentation mutated raw history evidence")
	}
	quoted := "Quoted example: `" + completionTestTrailer + "`"
	u = newAppServerSessionTestUI(t, t.TempDir())
	u.shell.Transcript.Conversation = true
	u.testActivity(u.shell.Transcript).testAudience()
	completionTestFinish(t, u, quoted)
	if !strings.Contains(paint(), "mekugi-reload-v1") {
		t.Fatal("quoted example was suppressed")
	}
}

func completionTestReplay(t *testing.T) *uiReplayPlayback {
	t.Helper()
	source := replayPlaybackTestSource()
	for i := range source.Events {
		if source.Events[i].Item.Type == "agentMessage" {
			if source.Events[i].Kind == observation.ItemCompleted {
				source.Events[i].Item.Text += "\n" + completionTestTrailer
			}
			if source.Events[i].Kind == observation.AgentDelta {
				source.Events[i].Delta += "\n" + completionTestTrailer[:16]
			}
		}
	}
	p := newTestUIReplayPlayback(new(activitymodel.Service), t.Context(), source, 1)
	t.Cleanup(p.close)
	return p
}

func completionTestHistory(count int) []binding.HistoryTurn {
	turns := make([]binding.HistoryTurn, count)
	for i := range turns {
		turns[i] = binding.HistoryTurn{ID: fmt.Sprintf("turn-%d", i), Status: "completed", Items: []binding.ThreadItem{{ID: fmt.Sprintf("answer-%d", i), Type: "agentMessage", Text: fmt.Sprintf("Restored answer %03d\n", i) + completionTestTrailer}}}
	}
	return turns
}
