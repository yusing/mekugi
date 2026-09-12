package router

import (
	"bytes"
	"strings"
	"testing"
)

func TestChildTokenUsageProjectsToRoot(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, outcome := range []string{"completed", "failed", "incomplete", "missing-usage", "commentary-only"} {
			t.Run(map[bool]string{false: "json", true: "sse"}[stream]+"/"+outcome, func(t *testing.T) {
				proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
				root, _ := prepareActivityTest(t, proxy, "shared-session", "root", "", "/root", nil)
				other, _ := prepareActivityTest(t, proxy, "other-session", "other-root", "", "/root", nil)
				child, _ := prepareActivityTest(t, proxy, "shared-session", "child", "root", "/root/worker", nil)
				if outcome == "completed" {
					if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, child.directory, child.shellThreadID, "", []journalMutation{{Op: "add", Text: new("Child milestone")}}); err != nil {
						t.Fatal(err)
					}
				}
				root.drainActivity() // Discard the unrelated start notice.
				root.Close()         // Reports must survive until the next root response.

				answer := map[string]any{
					"type": "message", "id": "answer", "role": "assistant",
					"phase": "final_answer", "status": "completed",
					"content": []any{map[string]any{"type": "output_text", "text": "Child result."}},
				}
				if outcome == "commentary-only" {
					answer["phase"] = "commentary"
				}
				item := answer
				if outcome == "completed" {
					item = journalFinishCall(`{"op":"finish"}`)
				}
				status := "completed"
				if outcome == "failed" || outcome == "incomplete" {
					status = outcome
				}
				response := map[string]any{
					"id": "child-response", "status": status, "output": []any{item},
				}
				if outcome != "missing-usage" {
					response["usage"] = map[string]any{
						"input_tokens": 120, "output_tokens": 30,
						"input_tokens_details":  map[string]any{"cached_tokens": 80},
						"output_tokens_details": map[string]any{"reasoning_tokens": 20},
					}
					observeTestResponseUsage(t, child, mustTestJSON(t, response), false)
				}

				var childOutput []byte
				for range 2 { // Repeated terminal observations must not duplicate root reports.
					if stream {
						itemEvent := mustTestJSON(t, map[string]any{
							"type": "response.output_item.done", "output_index": 0, "item": item,
						})
						if _, err := child.TransformSSE(itemEvent); err != nil {
							t.Fatal(err)
						}
						response["output"] = []any{} // Eligibility comes from streamed items.
						events, err := child.TransformSSE(mustTestJSON(t, map[string]any{
							"type": "response." + status, "response": response,
						}))
						if err != nil {
							t.Fatal(err)
						}
						for _, event := range events {
							child.Delivered(event)
						}
						child.ReleaseDelivery()
						childOutput = bytes.Join(events, nil)
					} else {
						var err error
						childOutput, err = child.TransformJSON(mustTestJSON(t, response))
						if err != nil {
							t.Fatal(err)
						}
						child.Delivered(childOutput)
						child.ReleaseDelivery()
					}
				}
				wantUsage := outcome == "completed"
				if bytes.Contains(childOutput, []byte("Tokens:")) != wantUsage {
					t.Fatalf("child usage eligibility: %s", childOutput)
				}
				if wantUsage && (bytes.Contains(childOutput, []byte("Child result.")) ||
					!bytes.Contains(childOutput, []byte("Journal saved:")) ||
					bytes.Index(childOutput, []byte("Tokens:")) >= bytes.Index(childOutput, []byte("Journal saved:"))) {
					t.Fatalf("usage did not precede the synthetic child result: %s", childOutput)
				}
				child.Close()

				root, _ = prepareActivityTest(t, proxy, "remapped-session", "root", "", "/root", nil)
				if !stream && wantUsage {
					requestJournalFinish(t, root)
				}
				rootResponse := []byte(`{"id":"root-response","status":"completed","output":[]}`)
				var output []byte
				if stream {
					events, err := root.TransformSSE([]byte(`{"type":"response.created","response":{"id":"root-response"}}`))
					if err != nil {
						t.Fatal(err)
					}
					output = bytes.Join(events, nil)
				} else {
					var err error
					output, err = root.TransformJSON(rootResponse)
					if err != nil {
						t.Fatal(err)
					}
				}
				if got := bytes.Count(output, []byte("Tokens:")); got != map[bool]int{false: 0, true: 1}[wantUsage] {
					t.Fatalf("root usage report count = %d: %s", got, output)
				}
				if bytes.Contains(childOutput, []byte("Journal flush")) {
					t.Fatalf("child completed with a premature flush: %s", childOutput)
				}
				if stream && bytes.Contains(output, []byte("Child milestone")) {
					t.Fatalf("child journal appeared before main completion: %s", output)
				}
				if !stream && wantUsage && !bytes.Contains(output, []byte("Child milestone")) {
					t.Fatalf("main completion lost child journal: %s", output)
				}
				root.ReleaseDelivery()
				if wantUsage {
					counts, _ := child.threadUsageCounts()
					want := "[`/root/worker`] " + formatTokenUsageReport(counts)
					if !strings.Contains(string(output), string(mustMarshalJSON(want))) {
						t.Fatalf("root report lost child attribution or totals: %s", output)
					}
					var replay []any
					for _, message := range root.activityMessages {
						replay = append(replay, message)
					}
					replay = append(replay, answer)
					_, request := prepareActivityTest(t, proxy, "replay-session", "root", "", "/root", replay)
					if bytes.Contains(request.fields["input"], []byte("Tokens:")) || !bytes.Contains(request.fields["input"], []byte("Child result.")) {
						t.Fatalf("replay filtering changed substantive output: %s", request.fields["input"])
					}
				}
				next, _ := prepareActivityTest(t, proxy, "next-session", "root", "", "/root", nil)
				for _, target := range []*mekugiResponseTransform{other, next} {
					visible, err := target.TransformJSON(rootResponse)
					if err != nil || bytes.Contains(visible, []byte("Tokens:")) {
						t.Fatalf("report repeated or reached another root: %s, %v", visible, err)
					}
				}
			})
		}
	}
}
