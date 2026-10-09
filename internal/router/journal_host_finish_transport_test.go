package router

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestJournalFinishCandidateTransaction(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, tasks, reset, state   string
		stopped, child, stale, want bool
	}{
		{name: "pending", tasks: `"Work"`},
		{name: "working", tasks: `{"title":"Work","state":"working"}`},
		{name: "nested", tasks: `{"title":"Work","tasks":[{"title":"Nested"}]}`},
		{name: "completes candidate", tasks: `"Work"`, state: "done", want: true},
		{name: "blocked report", tasks: `"Work"`, state: "blocked", want: true},
		{name: "slice boundary", tasks: `{"title":"First","state":"working"},"Second"`, reset: "slice", state: "done", want: true},
		{name: "old slice boundary", tasks: `{"title":"First","state":"done"},"Second"`, reset: "slice", stale: true},
		{name: "user stopped", tasks: `"Work"`, stopped: true, want: true},
		{name: "child local work", tasks: `"Work"`, child: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			transform, proxy, _, workspace := newDurableTreeTransform(t)
			thread := transform.shellThreadID
			var tasks []jsontext.Value
			if err := json.Unmarshal([]byte(`[`+test.tasks+`]`), &tasks); err != nil {
				t.Fatal(err)
			}
			if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, thread, "", []journalMutation{{Op: "plan", Reset: test.reset, Tasks: tasks}}); err != nil {
				t.Fatal(err)
			}
			transform.shellTurnID = "finish-turn"
			if err := proxy.journals.beginJournalTurn(t.Context(), proxy.replayStore, workspace, thread, transform.shellTurnID); err != nil {
				t.Fatal(err)
			}
			if test.stopped {
				if err := proxy.journals.stopJournalTurn(t.Context(), proxy.replayStore, workspace, thread, transform.shellTurnID); err != nil {
					t.Fatal(err)
				}
			}
			if test.child {
				if err := proxy.journals.transaction(t.Context(), proxy.replayStore, workspace, thread, func(j *threadJournal, _ bool) error { j.Parent = "parent"; return nil }); err != nil {
					t.Fatal(err)
				}
			}
			before, _, err := readThreadJournal(proxy.replayStore, workspace, thread)
			if err != nil {
				t.Fatal(err)
			}
			mutations := []journalMutation{{Op: "log", Text: new("Final report")}}
			if test.state != "" {
				mutations = append(mutations, journalMutation{Op: "set", P: "/1", State: &test.state, Reason: new("Needs input")})
			}
			if !test.want {
				candidate := append(slices.Clone(mutations), journalMutation{Op: "finish", finishTurn: transform.shellTurnID})
				_, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, thread, "candidate", candidate)
				path := "/1"
				if test.name == "nested" {
					path = "/1/1"
				}
				if test.stale {
					path = "/2"
				}
				if err == nil || !strings.Contains(err.Error(), "runnable task "+path+" (") {
					t.Fatalf("missing task correction: %v", err)
				}
				rejected, _, readErr := readThreadJournal(proxy.replayStore, workspace, thread)
				if readErr != nil || !reflect.DeepEqual(before.Items, rejected.Items) || !reflect.DeepEqual(before.Events, rejected.Events) || before.Sequence != rejected.Sequence || before.NextID != rejected.NextID || !reflect.DeepEqual(before.Receipts, rejected.Receipts) {
					t.Fatalf("rejected finish changed journal: %v", readErr)
				}
			}
			if !test.want {
				return
			}
			mutations = append(mutations, journalMutation{Op: "finish", finishTurn: transform.shellTurnID})
			receiptID := "runtime:" + journalHostFinishReceipt(transform.shellTurnID, "final-work")
			if _, err := proxy.applyJournal(t.Context(), workspace, thread, receiptID, mutations); err != nil {
				t.Fatal(err)
			}
			after, _, readErr := readThreadJournal(proxy.replayStore, workspace, thread)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if _, ok := after.Receipts[receiptID]; !ok || after.Sequence == before.Sequence {
				t.Fatal("candidate completion lost mutations or finish receipt")
			}

		})
	}
}
