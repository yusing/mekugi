package launch

import (
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"fmt"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversation "github.com/yusing/mekugi/modules/conversation/presentation"
)

func TestUISnapshotNativeSocketMainComposer(t *testing.T) {
	for _, width := range []int{36, 80} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.shell.Transcript.Painter.Theme = diffrender.DarkTheme
			u.attached.presentation.Status.Text, u.model, u.reasoningEffort = "Ready", "snapshot-model", "high"
			now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)
			u.clock = func() time.Time { return now }
			u.testActivity(u.shell.Transcript).Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{
				{Seq: 1, Agent: "You", Kind: "text", Text: "Explain the snapshot workflow.", Observed: now},
				{Seq: 2, Agent: "Main", Kind: "final", Text: "Review the **rendered frame**, accept its baseline, then rerun the offline check.", Observed: now},
			}})
			u.testMainFrame(width, 16, 0)
			frame := sessionapi.NativeFrame{
				View:   u.attached.presentation.View(),
				Main:   u.attached.source.Publication(u.attached.presentation, nil),
				Agents: u.attached.childSource.Publication(u.attached.presentation, u.attached.source),
			}
			wire, err := json.Marshal(frame, jsonv1.FormatDurationAsNano(true))
			if err != nil {
				t.Fatal(err)
			}
			var received sessionapi.NativeFrame
			if err := json.Unmarshal(wire, &received, jsonv1.FormatDurationAsNano(true)); err != nil {
				t.Fatal(err)
			}
			terminal, _ := u.terminalViews.NewAttachment(diffrender.DarkTheme)
			terminal.Transcript.Painter.Theme = diffrender.DarkTheme
			renderer := nativeClientRenderer{terminal: terminal, views: u.terminalViews}
			local := conversation.DraftView{Draft: composer.Draft{Text: "Add narrow-screen coverage.\nKeep the interaction assertions."}, Revision: 11}
			renderer.update(received, local, conversation.PromptDraftView{}, now)
			painted := terminal.Main.Render(terminal.Transcript, &terminal.Transcript.Painter, terminal.Prompts, terminalsession.MainOptions{Now: now, Focused: true, Version: mekugiVersion()}, width, 16, 0)
			assertNativeUISnapshot(t, fmt.Sprintf("native-main-%d", width), painted.Rows)
			// A later publication cannot replace client-local text or caret state.
			received.View.Version.Revision++
			received.View.Draft.Draft.Text = "foreign editor contents"
			renderer.update(received, local, conversation.PromptDraftView{}, now)
			painted = terminal.Main.Render(terminal.Transcript, &terminal.Transcript.Painter, terminal.Prompts, terminalsession.MainOptions{Now: now, Focused: true, Version: mekugiVersion()}, width, 16, 0)
			assertNativeUISnapshot(t, fmt.Sprintf("native-main-%d", width), painted.Rows)
		})
	}
}
