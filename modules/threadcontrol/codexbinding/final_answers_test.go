package codexbinding

import (
	"fmt"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestFinalDeliveryRetainsEveryCompletedAnswer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		driver, host, session, _ := reconciliationFixture(t)
		if err := driver.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		defer driver.Stop(t.Context())
		host.inputs = nil
		full := "## Summary\n\n" + strings.Repeat("handoff 世界\n", 300)
		observe := func(method, raw string) {
			t.Helper()
			if _, err := driver.Observe(method, []byte(raw)); err != nil {
				t.Fatal(err)
			}
		}
		for _, answer := range []struct{ id, phase, text string }{
			{"full", "final_answer", full},
			{"full", "final_answer", full}, // Duplicate observation, same native item.
			{"progress", "commentary", "not part of the completion"},
			{"short", "final_answer", "Complete. Full handoff is above."},
			{"empty", "final_answer", ""},
		} {
			observe("item/completed", fmt.Sprintf(`{"threadId":"child","turnId":"child-turn","item":{"type":"agentMessage","id":%q,"text":%q,"phase":%q}}`, answer.id, answer.text, answer.phase))
		}
		synctest.Wait()
		// The complete answer bytes survive a retained checkpoint.
		restored := control.New()
		if err := restored.Restore(t.Context(), session); err != nil {
			t.Fatal(err)
		}
		member, _ := restored.Member("child")
		if member.LastAnswer != full+"\n\nComplete. Full handoff is above." {
			t.Fatal("retained answer bytes changed")
		}
		observe("turn/completed", `{"threadId":"child","turn":{"id":"child-turn","status":"completed"}}`)
		observe("turn/completed", `{"threadId":"child","turn":{"id":"child-turn","status":"completed"}}`)
		synctest.Wait()
		if failures := driver.Failures(); len(failures) != 0 {
			t.Fatal(failures)
		}
		want := full + "\n\nComplete. Full handoff is above."
		if len(host.inputs) != 1 || host.inputs[0].Kind != "FINAL_ANSWER" || host.inputs[0].Message != want || host.inputs[0].Run {
			t.Fatalf("final handoff changed or repeated: inputs=%d want_bytes=%d", len(host.inputs), len(want))
		}
	})
}
