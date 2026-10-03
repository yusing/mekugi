package router

import (
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"reflect"
	"strings"
	"testing"
)

// Keep transport checks at the provider boundary: an ordinary answer must have
// the same message object, not merely appear somewhere in a generated card.
func separateReportFinal(t *testing.T, transform *mekugiResponseTransform, stream bool, id, body string) ([]map[string]jsonv1.RawMessage, [][]byte) {
	t.Helper()
	response := regressionFinalResponse(t, id, body)
	var terminal struct {
		Output []map[string]jsonv1.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(response, &terminal); err != nil {
		t.Fatal(err)
	}
	var frames [][]byte
	if stream {
		events, err := transform.TransformSSE(mustTestJSON(t, map[string]any{
			"type": "response.output_item.done", "output_index": 0, "item": terminal.Output[0],
		}))
		if err != nil {
			t.Fatal(err)
		}
		frames = append(frames, events...)
		// The host releases each streamed delivery lease before advancing to
		// the next event, even when its receipt remains unacknowledged.
		transform.ReleaseDelivery()
		events, err = transform.TransformSSE(mustTestJSON(t, map[string]any{
			"type": "response.completed", "response": jsonv1.RawMessage(response),
		}))
		if err != nil {
			t.Fatal(err)
		}
		frames = append(frames, events...)
		completed, answerDone := 0, 0
		for _, frame := range frames {
			var event struct {
				Type     string                       `json:"type"`
				Item     map[string]jsonv1.RawMessage `json:"item"`
				Response struct {
					Output []map[string]jsonv1.RawMessage `json:"output"`
				} `json:"response"`
			}
			if err := json.Unmarshal(frame, &event); err != nil {
				t.Fatal(err)
			}
			if event.Type == "response.completed" {
				completed++
				terminal.Output = event.Response.Output
			}
			if event.Type == "response.output_item.done" && jsonString(event.Item, "id") == "raw-"+id {
				answerDone++
			}
		}
		if completed != 1 || answerDone > 1 {
			t.Fatalf("completion lifecycle duplicated: completed=%d answerDone=%d", completed, answerDone)
		}
		for _, message := range terminal.Output {
			if jsonString(message, "id") == "raw-"+id && answerDone != 1 {
				t.Fatalf("preserved final has no output_item.done: %s", mustTestJSON(t, terminal.Output))
			}
		}
	} else {
		wire, err := transform.TransformJSON(response)
		if err != nil {
			t.Fatal(err)
		}
		frames = [][]byte{wire}
		if err := json.Unmarshal(wire, &terminal); err != nil {
			t.Fatal(err)
		}
	}
	if transform.journalContinue {
		t.Fatal("separate journal reporting requested another provider completion")
	}
	return terminal.Output, frames
}

func separateReportPreservedAnswer(t *testing.T, messages []map[string]jsonv1.RawMessage, id, body string, want bool) {
	t.Helper()
	var original struct {
		Output []map[string]jsonv1.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(regressionFinalResponse(t, id, body), &original); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, message := range messages {
		if jsonString(message, "id") != "raw-"+id {
			continue
		}
		count++
		var actual, expected map[string]any
		if err := json.Unmarshal(mustTestJSON(t, message), &actual); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(mustTestJSON(t, original.Output[0]), &expected); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(actual, expected) {
			t.Fatalf("ordinary provider answer changed: got=%v want=%v", actual, expected)
		}
	}
	wantCount := 0
	if want {
		wantCount = 1
	}
	if count != wantCount {
		t.Fatalf("ordinary final count=%d want=%d: %s", count, wantCount, mustTestJSON(t, messages))
	}
}

func separateReportRead(t *testing.T, proxy *mekugiProxy, workspace, thread string) threadJournal {
	t.Helper()
	journal, exists, err := readThreadJournal(proxy.replayStore, workspace, thread)
	if err != nil || !exists {
		t.Fatalf("durable journal missing: exists=%v err=%v", exists, err)
	}
	return journal
}

func separateReportDeliver(transform *mekugiResponseTransform, frames [][]byte) {
	for _, frame := range frames {
		transform.Delivered(frame)
	}
	transform.ReleaseDelivery()
}

func TestJournalSeparateReportAnswerOnlyPreservesFinal(t *testing.T) {
	t.Parallel()
	for _, native := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			t.Run(map[bool]string{false: "inline", true: "native"}[native]+"/"+map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
				transform, proxy, _, workspace := newDurableTreeTransform(t)
				thread := transform.shellThreadID
				var sink *nativeJournalSink
				if native {
					sink = proxy.journals.attachNative(workspace, thread)
					defer proxy.journals.detachNative(sink)
				}
				const final = "## Result\n\n| Key | Value |\n| --- | --- |\n| A | B |\n\n- Complete"
				messages, frames := separateReportFinal(t, transform, stream, "answer-only", final)
				separateReportPreservedAnswer(t, messages, "answer-only", final, true)
				if len(messages) != 1 {
					t.Fatalf("answer-only completion generated a report: %s", mustTestJSON(t, messages))
				}
				before := separateReportRead(t, proxy, workspace, thread)
				if len(before.Items) != 1 || before.Items[0].Kind != "answer" || before.Items[0].Text != final || before.Items[0].Flushed {
					t.Fatalf("answer not captured durably before delivery: %+v", before)
				}
				if sink != nil && (sink.hides("raw-answer-only") || len(sink.snapshot()) != 0) {
					t.Fatalf("native answer hidden or republished: %+v", sink.snapshot())
				}
				separateReportDeliver(transform, frames)
				after := separateReportRead(t, proxy, workspace, thread)
				if len(after.Items) != 1 || !after.Items[0].Flushed || !after.Items[0].Reported {
					t.Fatalf("delivered answer not acknowledged: %+v", after)
				}
				if sink != nil && len(sink.snapshot()) != 0 {
					t.Fatalf("answer-only delivery emitted native card: %+v", sink.snapshot())
				}
			})
		}
	}
}

func TestJournalSeparateReportKeepsFinalAndAcknowledgesExactSnapshot(t *testing.T) {
	t.Parallel()
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
			transform, proxy, _, workspace := newDurableTreeTransform(t)
			thread := transform.shellThreadID
			if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, thread, "seed", []journalMutation{
				{Op: "add", Kind: "task", Title: new("Repair parser"), State: new("working")},
				{Op: "log", P: "/1", Text: new("Parser evidence established")},
			}); err != nil {
				t.Fatal(err)
			}
			const final = "Substantive final, with **formatting** preserved."
			messages, _ := separateReportFinal(t, transform, stream, "report-retry", final)
			separateReportPreservedAnswer(t, messages, "report-retry", final, true)
			report := ""
			for _, message := range messages {
				if jsonString(message, "id") != "raw-report-retry" {
					report += commentaryMessageText(message)
				}
			}
			if !strings.Contains(report, "Parser evidence established") || strings.Contains(report, final) {
				t.Fatalf("report missing evidence or copied substantive final: %q", report)
			}
			before := separateReportRead(t, proxy, workspace, thread)
			if before.FlushSeq != 0 {
				t.Fatalf("withheld delivery acknowledged report: %+v", before)
			}
			transform.ReleaseDelivery()
			transform.Close()
			retry := resumeJournalRegression(t, proxy, workspace, thread)
			messages, frames := separateReportFinal(t, retry, stream, "report-retry", final)
			separateReportPreservedAnswer(t, messages, "report-retry", final, true)
			if len(messages) < 2 {
				t.Fatal("withheld report did not retry")
			}
			snapshot := separateReportRead(t, proxy, workspace, thread)
			answers := 0
			for _, item := range snapshot.Items {
				if item.Kind == "answer" {
					answers++
				}
			}
			if answers != 1 {
				t.Fatalf("retry duplicated durable answer: %+v", snapshot.Items)
			}
			// A mutation after preparation is not part of the delivered snapshot.
			retry.ReleaseDelivery()
			if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, thread, "later", []journalMutation{{Op: "log", P: "/1", Text: new("Later unseen evidence")}}); err != nil {
				t.Fatal(err)
			}
			separateReportDeliver(retry, frames)
			after := separateReportRead(t, proxy, workspace, thread)
			if after.FlushSeq != snapshot.Sequence || after.FlushSeq >= after.Sequence || after.Items[len(after.Items)-1].Flushed {
				t.Fatalf("delivery consumed a newer revision: snapshot=%d journal=%+v", snapshot.Sequence, after)
			}
		})
	}
}

func TestJournalSeparateReportEmptyOutcomeRequiresMeaningfulReport(t *testing.T) {
	t.Parallel()
	for _, native := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			for _, report := range []bool{false, true} {
				for _, final := range []string{"Done.", ""} {
					t.Run(map[bool]string{false: "inline", true: "native"}[native]+"/"+map[bool]string{false: "json", true: "sse"}[stream]+"/"+map[bool]string{false: "no-report", true: "report"}[report]+"/"+final, func(t *testing.T) {
						transform, proxy, _, workspace := newDurableTreeTransform(t)
						thread := transform.shellThreadID
						var sink *nativeJournalSink
						if native {
							sink = proxy.journals.attachNative(workspace, thread)
							defer proxy.journals.detachNative(sink)
						}
						if report {
							if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, thread, "milestone", []journalMutation{{Op: "add", Kind: "note", Title: new("Verified milestone")}}); err != nil {
								t.Fatal(err)
							}
						}
						messages, frames := separateReportFinal(t, transform, stream, "empty-outcome", final)
						// Native still transports the stock item; hiding belongs to the UI.
						separateReportPreservedAnswer(t, messages, "empty-outcome", final, stream || native || !report)
						if sink != nil {
							if sink.hides("raw-empty-outcome") != report {
								t.Fatal("native suppressed empty outcome without a meaningful report, or failed to suppress redundant outcome")
							}
						}
						separateReportDeliver(transform, frames)
						if sink != nil {
							cards := 0
							for _, pending := range sink.snapshot() {
								if pending.card != nil {
									cards++
								}
							}
							if (cards > 0) != report {
								t.Fatalf("native report eligibility=%v cards=%d", report, cards)
							}
						} else if report && len(messages) == 0 {
							t.Fatal("empty outcome suppressed without a replacement report")
						}
					})
				}
			}
		}
	}
}

func TestJournalSeparateReportNativeDeliveryAndMountDiagnostic(t *testing.T) {
	t.Parallel()
	for _, diagnostic := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			t.Run(map[bool]string{false: "events", true: "mount-diagnostic"}[diagnostic]+"/"+map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
				transform, proxy, _, workspace := newDurableTreeTransform(t)
				thread := transform.shellThreadID
				if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, thread, "seed", []journalMutation{{Op: "add", Kind: "task", Title: new("Open task"), State: new("working")}}); err != nil {
					t.Fatal(err)
				}
				if diagnostic {
					seed := separateReportRead(t, proxy, workspace, thread)
					if err := proxy.journals.acknowledgeTree(t.Context(), proxy.replayStore, workspace, thread, seed.Sequence, true); err != nil {
						t.Fatal(err)
					}
					if err := proxy.journals.initialize(t.Context(), proxy.replayStore, workspace, "unreadable-child", "/root/child", ""); err != nil {
						t.Fatal(err)
					}
					if err := proxy.journals.bindIdentity(t.Context(), proxy.replayStore, workspace, "unreadable-child", thread, "/root/child", true); err != nil {
						t.Fatal(err)
					}
					if err := proxy.journals.transaction(t.Context(), proxy.replayStore, workspace, "unreadable-child", func(j *threadJournal, _ bool) error {
						j.Receipts = nil
						return nil
					}); err != nil {
						t.Fatal(err)
					}
				}
				sink := proxy.journals.attachNative(workspace, thread)
				defer proxy.journals.detachNative(sink)
				const final = "Native substantive final must remain ordinary."
				messages, frames := separateReportFinal(t, transform, stream, "native-report", final)
				separateReportPreservedAnswer(t, messages, "native-report", final, true)
				if len(messages) != 1 || sink.hides("raw-native-report") {
					t.Fatalf("native generated carrier or hid substantive final: %s", mustTestJSON(t, messages))
				}
				for _, publication := range sink.snapshot() {
					if publication.card != nil {
						t.Fatal("native terminal report published before Delivered")
					}
				}
				before := separateReportRead(t, proxy, workspace, thread)
				separateReportDeliver(transform, frames)
				pending := sink.snapshot()
				cards := 0
				for _, publication := range pending {
					if publication.card == nil {
						continue
					}
					cards++
					text := journalTurnCard(publication.card.Journal, publication.card.Since, false)
					if strings.Contains(text, final) || (!diagnostic && !strings.Contains(text, "Open task")) || (diagnostic && !strings.Contains(text, "Mounted journals unavailable:")) {
						t.Fatalf("separate native card content: %q", text)
					}
				}
				if cards != 1 {
					t.Fatalf("terminal delivery published %d cards", cards)
				}
				if !diagnostic && separateReportRead(t, proxy, workspace, thread).FlushSeq != before.FlushSeq {
					t.Fatal("publication acknowledged native report before rendering")
				}
				if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, thread, "later", []journalMutation{{Op: "log", P: "/1", Text: new("Newer than rendered card")}}); err != nil {
					t.Fatal(err)
				}
				if err := sink.acknowledge(t.Context(), proxy, pending); err != nil {
					t.Fatal(err)
				}
				after := separateReportRead(t, proxy, workspace, thread)
				if after.FlushSeq != before.Sequence || after.Items[len(after.Items)-1].Flushed {
					t.Fatalf("native receipt acknowledged newer unseen revision: %+v", after)
				}
			})
		}
	}
}

func TestJournalSeparateReportAnswerOnlyAfterFreshRouterResumeAndFork(t *testing.T) {
	t.Parallel()
	for _, fork := range []bool{false, true} {
		t.Run(map[bool]string{false: "resume", true: "fork"}[fork], func(t *testing.T) {
			first, proxy, _, workspace := newDurableTreeTransform(t)
			thread := first.shellThreadID
			if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, thread, "open", []journalMutation{{Op: "add", Kind: "task", Title: new("Acknowledged open task"), State: new("working")}}); err != nil {
				t.Fatal(err)
			}
			seed := separateReportRead(t, proxy, workspace, thread)
			if err := proxy.journals.acknowledgeTree(t.Context(), proxy.replayStore, workspace, thread, seed.Sequence, true); err != nil {
				t.Fatal(err)
			}
			first.Close()
			fresh := newManagedMekugiProxy(t)
			var err error
			fresh.replayStore, err = openMekugiReplayStore(proxy.replayStore.directory)
			if err != nil {
				t.Fatal(err)
			}
			if fork {
				if err := fresh.journals.initialize(t.Context(), fresh.replayStore, workspace, "fork-main", "/root", thread); err != nil {
					t.Fatal(err)
				}
				thread = "fork-main"
			}
			resumed := resumeJournalRegression(t, fresh, workspace, thread)
			messages, frames := separateReportFinal(t, resumed, false, "resumed-answer", "Answer after restart.")
			separateReportPreservedAnswer(t, messages, "resumed-answer", "Answer after restart.", true)
			if len(messages) != 1 {
				t.Fatalf("unchanged acknowledged open task triggered a report: %s", mustTestJSON(t, messages))
			}
			separateReportDeliver(resumed, frames)
			// A separate Main cannot borrow the resumed thread's open-task state.
			other := resumeJournalRegression(t, fresh, workspace, "independent-main")
			messages, frames = separateReportFinal(t, other, false, "other-answer", "Independent answer.")
			if len(messages) != 1 {
				t.Fatalf("another thread's journal leaked into final: %s", mustTestJSON(t, messages))
			}
			separateReportDeliver(other, frames)
			isolated := separateReportRead(t, fresh, workspace, "independent-main")
			if len(isolated.Items) != 1 || isolated.Items[0].Kind != "answer" || isolated.Items[0].Text != "Independent answer." {
				t.Fatalf("durable thread state leaked: %+v", isolated.Items)
			}
		})
	}
}

func TestJournalStreamingReportDoesNotRerenderFinal(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"final_answer", ""} {
		t.Run(phase, func(t *testing.T) {
			transform, proxy, _, workspace := newDurableTreeTransform(t)
			if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, transform.shellThreadID, "seed", []journalMutation{{Op: "add", Kind: "note", Title: new("Verified change")}}); err != nil {
				t.Fatal(err)
			}
			answer := finalAnswerTestEvents(t, phase)
			var frames [][]byte
			for _, payload := range answer {
				events, err := transform.TransformSSE(payload)
				if err != nil {
					t.Fatal(err)
				}
				frames = append(frames, events...)
				for _, event := range events {
					transform.Delivered(event)
				}
				transform.ReleaseDelivery()
			}
			before := separateReportRead(t, proxy, workspace, transform.shellThreadID)
			if before.FlushSeq != 0 {
				t.Fatal("early report acknowledged without a terminal")
			}
			if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, transform.shellThreadID, "late", []journalMutation{{Op: "add", Kind: "note", Title: new("Late evidence")}}); err != nil {
				t.Fatal(err)
			}
			terminal, err := transform.TransformSSE(finalAnswerTestTerminal(t, "completed", false))
			if err != nil {
				t.Fatal(err)
			}
			defer transform.ReleaseDelivery()
			frames = append(frames, terminal...)
			// Source: codex-rs/tui/src/chatwidget/streaming.rs:419 and
			// protocol.rs:450@7135b303d (local host clone). Every done message changes
			// lastCompleted, even commentary; completion renders the final again when
			// the terminal snapshot's final ID differs.
			lastCompleted, reportCount, answerCount := "", 0, 0
			for _, payload := range frames {
				var event struct {
					Type     string                       `json:"type"`
					Item     map[string]jsonv1.RawMessage `json:"item"`
					Response struct {
						Output []map[string]jsonv1.RawMessage `json:"output"`
					} `json:"response"`
				}
				if err := json.Unmarshal(payload, &event); err != nil {
					t.Fatal(err)
				}
				if event.Type == "response.output_item.done" && jsonString(event.Item, "type") == "message" {
					lastCompleted = jsonString(event.Item, "id")
					if lastCompleted == "answer" {
						answerCount++
					} else {
						reportCount++
					}
					if strings.Contains(commentaryMessageText(event.Item), "Late evidence") {
						t.Fatal("late report followed the answer")
					}
				}
				if event.Type == "response.completed" {
					finalID := ""
					for _, item := range event.Response.Output {
						if isFinalAnswerMessage(item) {
							finalID = jsonString(item, "id")
						}
					}
					if finalID != lastCompleted || finalID != "answer" {
						t.Fatalf("host would rerender final %q after last completed %q", finalID, lastCompleted)
					}
				}
			}
			if reportCount != 1 || answerCount != 1 {
				t.Fatalf("report events=%d answer events=%d", reportCount, answerCount)
			}
			for _, payload := range terminal {
				transform.Delivered(payload)
			}
			after := separateReportRead(t, proxy, workspace, transform.shellThreadID)
			if after.FlushSeq != before.Sequence || after.Items[1].Flushed {
				t.Fatalf("terminal consumed unseen late evidence: %+v", after)
			}
		})
	}
}

func TestJournalStreamingEarlyReportRemainsPendingOnFailure(t *testing.T) {
	t.Parallel()
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, transform.shellThreadID, "seed", []journalMutation{{Op: "add", Kind: "note", Title: new("Verified change")}}); err != nil {
		t.Fatal(err)
	}
	for _, payload := range finalAnswerTestEvents(t, "final_answer") {
		events, err := transform.TransformSSE(payload)
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			transform.Delivered(event)
		}
		transform.ReleaseDelivery()
	}
	events, err := transform.TransformSSE(finalAnswerTestTerminal(t, "failed", false))
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		transform.Delivered(event)
	}
	transform.ReleaseDelivery()
	after := separateReportRead(t, proxy, workspace, transform.shellThreadID)
	if after.FlushSeq != 0 || after.Items[0].Flushed {
		t.Fatalf("failed response acknowledged early report: %+v", after)
	}
}
