package router

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestNaturalJournalAnswerCapturesOnceAndFlushes(t *testing.T) {
	transform, proxy, _, _ := newMekugiTestTransform(t)
	transform.journalQuestion = "What changed?"
	answer := map[string]any{"type": "message", "id": "answer-item", "role": "assistant", "phase": "final_answer", "status": "completed",
		"content": []any{map[string]any{"type": "output_text", "text": "The requested change is complete."}}}
	response := mustTestJSON(t, map[string]any{"id": "natural-answer", "status": "completed", "output": []any{answer}})
	visible, err := transform.TransformJSON(response)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Output []map[string]json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(visible, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Output) == 0 || !strings.Contains(commentaryMessageText(result.Output[len(result.Output)-1]), "**Question:**\n\nWhat changed?\n\n**Answer:**\n\nThe requested change is complete.") {
		t.Fatalf("missing question/answer flush: %s", visible)
	}
	for _, item := range result.Output {
		if jsonString(item, "id") == "answer-item" {
			t.Fatalf("raw answer also delivered: %s", visible)
		}
	}
	transform.Delivered(visible)
	transform.ReleaseDelivery()
	items, err := proxy.journals.list(t.Context(), proxy.replayStore, transform.directory, transform.shellThreadID)
	if err != nil || len(items) != 1 || items[0].Question != "What changed?" || !items[0].Flushed {
		t.Fatalf("answer was not durably acknowledged: %+v, %v", items, err)
	}
	if err := transform.captureNaturalJournalAnswer(response); err != nil {
		t.Fatal(err)
	}
	items, err = proxy.journals.list(t.Context(), proxy.replayStore, transform.directory, transform.shellThreadID)
	if err != nil || len(items) != 1 {
		t.Fatalf("answer replay duplicated: %+v, %v", items, err)
	}
}

func TestNaturalJournalAnswerRequiresSuccessfulHostFreeCompletion(t *testing.T) {
	for _, status := range []string{"failed", "incomplete", "completed"} {
		t.Run(status, func(t *testing.T) {
			transform, proxy, _, _ := newMekugiTestTransform(t)
			transform.journalQuestion = "Question?"
			answer := map[string]any{"type": "message", "id": "answer-item", "role": "assistant", "phase": "final_answer", "status": "completed",
				"content": []any{map[string]any{"type": "output_text", "text": "Answer."}}}
			output := []any{answer}
			if status == "completed" {
				output = append(output, map[string]any{"type": "function_call", "id": "host", "name": "lookup", "call_id": "host-call", "arguments": `{}`, "status": "completed"})
			}
			visible, err := transform.TransformJSON(mustTestJSON(t, map[string]any{"id": status, "status": status, "output": output}))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(visible), "Journal flush") {
				t.Fatalf("nonterminal response flushed: %s", visible)
			}
			items, err := proxy.journals.list(t.Context(), proxy.replayStore, transform.directory, transform.shellThreadID)
			if err != nil || len(items) != 0 {
				t.Fatalf("nonterminal answer persisted: %+v, %v", items, err)
			}
		})
	}
}

func TestNaturalJournalAnswerStreamsWhenTerminalStatusIsAbsent(t *testing.T) {
	transform, proxy, _, _ := newMekugiTestTransform(t)
	transform.journalQuestion = "What changed?"
	answer := map[string]any{"type": "message", "id": "answer-item", "role": "assistant", "phase": "final_answer", "status": "completed",
		"content": []any{map[string]any{"type": "output_text", "text": "Done."}}}
	first, err := transform.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.output_item.done", "item": answer}))
	if err != nil || len(first) != 0 {
		t.Fatalf("answer escaped before terminal: %s, %v", first, err)
	}
	last, err := transform.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.completed", "response": map[string]any{"id": "no-status", "output": []any{answer}}}))
	if err != nil {
		t.Fatal(err)
	}
	var terminal bool
	for _, event := range last {
		if strings.Contains(string(event), `"type":"response.completed"`) {
			terminal = true
		}
		if strings.Contains(string(event), `"id":"answer-item"`) {
			t.Fatalf("raw answer escaped: %s", last)
		}
		transform.Delivered(event)
	}
	transform.ReleaseDelivery()
	if !terminal || !bytes.Contains(bytes.Join(last, nil), []byte("**Question:**")) {
		t.Fatalf("missing terminal question/answer flush: %s", last)
	}
	items, err := proxy.journals.list(t.Context(), proxy.replayStore, transform.directory, transform.shellThreadID)
	if err != nil || len(items) != 1 || !items[0].Flushed {
		t.Fatalf("stream answer was not flushed: %+v, %v", items, err)
	}
}

func TestNaturalJournalAnswerBeyondItemLimitStillCompletes(t *testing.T) {
	for _, full := range []bool{false, true} {
		t.Run(map[bool]string{false: "long-question", true: "full-journal"}[full], func(t *testing.T) {
			transform, proxy, _, _ := newMekugiTestTransform(t)
			if full {
				mutations := make([]journalMutation, maxJournalItems)
				for index := range mutations {
					mutations[index] = journalMutation{Op: "add", Text: new("Milestone")}
				}
				if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, transform.directory, transform.shellThreadID, "fill", mutations); err != nil {
					t.Fatal(err)
				}
				transform.journalQuestion = "Question?"
			} else {
				transform.journalQuestion = strings.Repeat("Q", maxJournalItemBytes)
			}
			answer := map[string]any{"type": "message", "id": "answer-item", "role": "assistant", "phase": "final_answer", "status": "completed",
				"content": []any{map[string]any{"type": "output_text", "text": "Yes."}}}
			visible, err := transform.TransformJSON(mustTestJSON(t, map[string]any{"id": "over-limit", "status": "completed", "output": []any{answer}}))
			if err != nil || bytes.Contains(visible, []byte("Journal flush")) || !bytes.Contains(visible, []byte(`"id":"answer-item"`)) || !bytes.Contains(visible, []byte("Yes.")) {
				t.Fatalf("capacity fallback did not preserve raw answer: %s, %v", visible, err)
			}
			if transform.journalContinue {
				t.Fatalf("capacity fallback requested another provider turn: %s", visible)
			}
		})
	}
}

func TestNaturalJournalAnswerWithLocalJournalCall(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
			transform, _, _, _ := newMekugiTestTransform(t)
			transform.journalQuestion = "Question?"
			call := journalFinishCall(`{"op":"add","text":"Milestone"}`)
			answer := map[string]any{"type": "message", "id": "answer-item", "role": "assistant", "phase": "final_answer", "status": "completed",
				"content": []any{map[string]any{"type": "output_text", "text": "Done."}}}
			response := map[string]any{"id": "with-local", "status": "completed", "output": []any{call, answer}}
			var visible []byte
			if stream {
				for _, item := range []any{call, answer} {
					if _, err := transform.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.output_item.done", "item": item})); err != nil {
						t.Fatal(err)
					}
				}
				events, err := transform.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.completed", "response": response}))
				if err != nil {
					t.Fatal(err)
				}
				visible = bytes.Join(events, nil)
			} else {
				var err error
				visible, err = transform.TransformJSON(mustTestJSON(t, response))
				if err != nil {
					t.Fatal(err)
				}
			}
			if transform.journalContinue || !bytes.Contains(visible, []byte("Journal flush")) || !bytes.Contains(visible, []byte("Milestone")) || !bytes.Contains(visible, []byte("**Answer:**")) {
				t.Fatalf("local call forced continuation or lost answer: %s", visible)
			}
		})
	}
}

func TestNaturalJournalAnswerCapacityFallbackWithLocalCall(t *testing.T) {
	transform, _, _, _ := newMekugiTestTransform(t)
	transform.journalQuestion = strings.Repeat("Q", maxJournalItemBytes)
	call := journalFinishCall(`{"op":"add","text":"Milestone"}`)
	answer := map[string]any{"type": "message", "id": "answer-item", "role": "assistant", "phase": "final_answer", "status": "completed",
		"content": []any{map[string]any{"type": "output_text", "text": "Done."}}}
	visible, err := transform.TransformJSON(mustTestJSON(t, map[string]any{"id": "local-over-limit", "status": "completed", "output": []any{call, answer}}))
	if err != nil || transform.journalContinue || !bytes.Contains(visible, []byte(`"id":"answer-item"`)) || bytes.Contains(visible, []byte("Journal flush")) {
		t.Fatalf("capacity fallback lost final answer or requested another turn: %s, %v", visible, err)
	}
}

func TestNaturalJournalAnswerKeepsBufferedCommentary(t *testing.T) {
	transform, _, _, _ := newMekugiTestTransform(t)
	commentary := map[string]any{"type": "message", "id": "commentary-item", "role": "assistant", "phase": "commentary", "status": "completed",
		"content": []any{map[string]any{"type": "output_text", "text": "Progress."}}}
	unknown := map[string]any{"type": "message", "id": "commentary-item", "role": "assistant", "status": "in_progress"}
	answer := map[string]any{"type": "message", "id": "answer-item", "role": "assistant", "phase": "final_answer", "status": "completed",
		"content": []any{map[string]any{"type": "output_text", "text": "Done."}}}
	for _, event := range []any{
		map[string]any{"type": "response.output_item.added", "output_index": 0, "item": unknown},
		map[string]any{"type": "response.output_text.delta", "output_index": 0, "delta": "Progress."},
		map[string]any{"type": "response.output_item.done", "output_index": 0, "item": commentary},
		map[string]any{"type": "response.output_item.done", "output_index": 1, "item": answer},
	} {
		if _, err := transform.TransformSSE(mustTestJSON(t, event)); err != nil {
			t.Fatal(err)
		}
	}
	events, err := transform.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.completed", "response": map[string]any{"id": "mixed-stream", "status": "completed", "output": []any{commentary, answer}}}))
	if err != nil {
		t.Fatal(err)
	}
	var commentaryDone, answerDone, commentaryDelta bool
	for _, payload := range events {
		var event struct {
			Type string                     `json:"type"`
			Item map[string]json.RawMessage `json:"item"`
		}
		_ = json.Unmarshal(payload, &event)
		commentaryDone = commentaryDone || event.Type == "response.output_item.done" && jsonString(event.Item, "id") == "commentary-item"
		answerDone = answerDone || event.Type == "response.output_item.done" && jsonString(event.Item, "id") == "answer-item"
		commentaryDelta = commentaryDelta || event.Type == "response.output_text.delta"
	}
	if !commentaryDone || !commentaryDelta || answerDone {
		t.Fatalf("buffered commentary lifecycle lost or raw answer escaped: %s", events)
	}
}
