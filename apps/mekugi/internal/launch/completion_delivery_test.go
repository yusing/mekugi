package launch

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/codex/appserver"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestFinalWaitAndPreparedChangesPublishTogether(t *testing.T) {
	for _, failure := range []string{"", "host rejected wake"} {
		t.Run(failure, func(t *testing.T) {
			u, threads, at := logicalWaitUI(t, nil)
			service := threads.shared
			check := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			check(service.AnswerCompleted(t.Context(), "child", "child-turn", "answer", "Completed work."))
			check(service.HostCompleted(t.Context(), "child", "child-turn", control.Completed, control.HostFailure{}))
			effects, _, err := service.PendingEffects("child")
			check(err)
			if len(effects) != 1 {
				t.Fatalf("final effects: %+v", effects)
			}
			effect, err := service.ClaimEffect(t.Context(), effects[0].Caller, effects[0].ID)
			check(err)
			input := control.NativeInput{Thread: "main", ID: effect.ID, Kind: "FINAL_ANSWER", Outcome: control.Completed, Sender: "/root/worker", Recipient: "/root", Message: effect.Message}
			prepared := sessioncontrol.PreparedCompletion{Source: "reader", Reload: []activity.ReloadSource{{Changes: "amber1..3", Preload: true, Command: "mreload selection", ExitCode: new(int)}}}
			stageNativeDelivery(&u.attached, input, prepared, *at)
			flush := func() {
				check(u.attached.threadInput.flushCompletions(func(appserver.Message) error { t.Fatal("unexpected host completion"); return nil }))
				syncLogicalWork(&u.attached, &u.Live, threads, *at)
			}
			publication := func() string {
				view := u.testActivity(u.shell.Transcript)
				view.Conversation = true
				return ansi.Strip(strings.Join(view.testRenderConversation(100).Lines, "\n"))
			}
			assertPending := func() {
				flush()
				if text := publication(); strings.Contains(text, "Completed work.") || strings.Contains(text, "Attached changes") || strings.Contains(text, "Finished waiting") {
					t.Fatalf("partial delivery became visible:\n%s", text)
				}
			}
			assertPending()
			check(service.EffectAcknowledged(t.Context(), "child", effect.ID, control.Receipt{}))
			wake, err := service.PrepareWake(t.Context(), "main", *at)
			check(err)
			if wake == nil {
				t.Fatal("missing completion wake")
			}
			assertPending() // Queued is not host-confirmed delivery.
			_, err = service.ClaimEffect(t.Context(), "main", wake.ID)
			check(err)
			assertPending()
			check(service.EffectAcknowledged(t.Context(), "main", wake.ID, control.Receipt{Turn: "continued", Error: failure}))
			flush()
			text := publication()
			for _, expected := range []string{"Completed work.", "Attach", "amber1..3"} {
				if !strings.Contains(text, expected) {
					t.Fatalf("coherent delivery lost %q:\n%s", expected, text)
				}
			}
			if failure == "" && !strings.Contains(text, "Finished waiting") || failure != "" && (strings.Contains(text, "Finished waiting") || service.WaitPublication("main").State != "failed") {
				t.Fatalf("wake outcome was misrepresented:\n%s", text)
			}
			before := len(u.attached.source.Entries)
			flush()
			if len(u.attached.source.Entries) != before {
				t.Fatal("completion republished on a subsequent observation")
			}
		})
	}
}
