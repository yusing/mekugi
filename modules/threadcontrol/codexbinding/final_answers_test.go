package codexbinding

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/agentguidance/compact"
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
		// Captured Codex async-question shape: phase alone cannot identify an
		// authored answer. Keep it out before retaining or delivering completion.
		observe("item/completed", `{"threadId":"child","turnId":"child-turn","item":{"type":"agentMessage","id":"question","text":"Which interruption steps?","phase":"final_answer","delivery":"async","questions":[{"title":"Which interruption steps?","options":null}]}}`)
		synctest.Wait()
		if member, _ := driver.control.Member("child"); member.LastAnswer != "" {
			t.Fatal("async question became an authored answer")
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
		if self, _, _ := driver.control.FeatureUse("child"); self.CompletionChanges != 0 {
			t.Fatal("an answer without an edit range counted as one")
		}
	})
}

func TestFinalDeliveryCountsSuppliedEditRanges(t *testing.T) {
	for _, tc := range []struct {
		name, selection string
		want            uint64
	}{
		{"changes", `{"sources":[{"changes":["blue1..blue3"]}]}`, 1},
		{"empty", `{"sources":[]}`, 0},
		{"file", `{"sources":[{"path":"main.go"}]}`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				driver, host, _, _ := reconciliationFixture(t)
				if err := driver.Start(t.Context()); err != nil {
					t.Fatal(err)
				}
				defer driver.Stop(t.Context())
				host.inputs = nil
				text := "Done.\n" + compact.CompactMetadataStart + tc.selection + compact.CompactMetadataEnd
				for _, event := range []struct{ method, raw string }{
					{"item/completed", fmt.Sprintf(`{"threadId":"child","turnId":"child-turn","item":{"type":"agentMessage","id":"final","text":%q,"phase":"final_answer"}}`, text)},
					{"turn/completed", `{"threadId":"child","turn":{"id":"child-turn","status":"completed"}}`},
				} {
					if _, err := driver.Observe(event.method, []byte(event.raw)); err != nil {
						t.Fatal(err)
					}
				}
				synctest.Wait()
				if len(host.inputs) != 1 || host.inputs[0].Kind != "FINAL_ANSWER" {
					t.Fatalf("final delivery = %+v", host.inputs)
				}
				if self, _, _ := driver.control.FeatureUse("child"); self.CompletionChanges != tc.want || self.Finals != 1 {
					t.Fatalf("edit ranges counted %d of %d finals, want %d of 1", self.CompletionChanges, self.Finals, tc.want)
				}
			})
		})
	}
}

func TestReconciledFinalDeliveryCountsEditRange(t *testing.T) {
	driver, host, _, _ := reconciliationFixture(t)
	text := "Done.\n" + compact.CompactMetadataStart + `{"sources":[{"changes":["blue1..blue3"]}]}` + compact.CompactMetadataEnd
	if err := driver.control.AnswerCompleted(t.Context(), "child", "child-turn", "answer", text); err != nil {
		t.Fatal(err)
	}
	if err := driver.control.HostCompleted(t.Context(), "child", "child-turn", control.Completed, control.HostFailure{}); err != nil {
		t.Fatal(err)
	}
	host.finalFailure = &binding.DispatchError{Outcome: binding.Uncertain, Cause: errors.New("lost final acknowledgement")}
	if err := driver.DispatchPending(t.Context(), "child"); err == nil {
		t.Fatal("delivery with lost acknowledgement succeeded")
	}
	if self, _, _ := driver.control.FeatureUse("child"); self.CompletionChanges != 0 || self.Finals != 0 {
		t.Fatalf("unconfirmed delivery counted %+v", self)
	}
	host.finalFailure = nil
	for range 2 {
		if err := driver.Reconcile(t.Context(), "root"); err != nil {
			t.Fatal(err)
		}
	}
	if len(host.confirmed) != 1 || host.confirmed[0].Kind != "FINAL_ANSWER" {
		t.Fatalf("confirmations = %+v", host.confirmed)
	}
	if self, _, _ := driver.control.FeatureUse("child"); self.CompletionChanges != 1 || self.Finals != 1 {
		t.Fatalf("reconciled delivery counted %+v, want one final with edit ranges", self)
	}
}
