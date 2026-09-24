package router

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestJournalAnswerDurabilityReplayAndEditing(t *testing.T) {
	replay, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := newJournalStore()
	if err := store.initialize(t.Context(), replay, "/workspace", "root", "/root", ""); err != nil {
		t.Fatal(err)
	}
	mutation := journalMutation{Op: "add", Text: new("Answer"), Answer: new(true)}
	apply := func(thread, receipt, question string, mutations ...journalMutation) error {
		_, err := store.apply(t.Context(), replay, "/workspace", thread, receipt, bindJournalAnswers(mutations, question))
		return err
	}
	if err := apply("root", "add", "Original?", mutation); err != nil {
		t.Fatal(err)
	}
	store = newJournalStore()
	if err := apply("root", "add", "Later?", mutation); err != nil {
		t.Fatal(err)
	}
	if err := apply("root", "edit", "", journalMutation{Op: "edit", ID: "amber", Text: new("Revised")}); err != nil {
		t.Fatal(err)
	}
	if err := store.initialize(t.Context(), replay, "/workspace", "fork", "/root", "root"); err != nil {
		t.Fatal(err)
	}
	if err := apply("root", "clear", "", journalMutation{Op: "edit", ID: "amber", Text: new("Plain"), Answer: new(false)}); err != nil {
		t.Fatal(err)
	}
	for thread, want := range map[string]string{"root": "", "fork": "Original?"} {
		items, err := newJournalStore().list(t.Context(), replay, "/workspace", thread)
		if err != nil || len(items) != 1 || items[0].Question != want {
			t.Fatalf("%s: %+v %v", thread, items, err)
		}
	}
	if err := apply("root", "without-source", "", mutation); err != nil {
		t.Fatalf("answer without a source question was rejected: %v", err)
	}
	for _, question := range []string{"\xff", strings.Repeat("q", maxJournalItemBytes)} {
		if err := apply("root", "", question, mutation); err == nil {
			t.Fatalf("accepted invalid source of %d bytes", len(question))
		}
	}
	if err := apply("fork", "", "", journalMutation{Op: "edit", ID: "amber", Text: new(strings.Repeat("a", maxJournalItemBytes))}); err == nil {
		t.Fatal("preserved question was omitted from item budget")
	}
	if err := apply("root", "", "Next?", journalMutation{Op: "edit", ID: "amber", Text: new("New answer"), Answer: new(true)}); err != nil {
		t.Fatal(err)
	}
	items, err := store.list(t.Context(), replay, "/workspace", "root")
	if err != nil || items[0].Question != "Next?" {
		t.Fatalf("reassociation: %+v %v", items, err)
	}
	for _, answer := range []bool{true, false} {
		if err := apply("root", "", "Question", journalMutation{Op: "delete", ID: "amber", Answer: new(answer)}); err == nil {
			t.Fatal("accepted answer on delete")
		}
	}
	if _, err := decodeJournalMutations([]byte(`[{"op":"add","text":"answer","question":"invented"}]`)); err == nil {
		t.Fatal("accepted authored question")
	}
}

func TestJournalMutationRoutingAndRendering(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	transform, _, _, _ := newMekugiTestTransformWithProxy(t, proxy)
	transform.journalQuestion = "Which?\n\n- A\n- B"
	call := func(id, arguments string) map[string]json.RawMessage {
		t.Helper()
		result, err := transform.executeJournalCall(map[string]json.RawMessage{
			"type": mustMarshalJSON("function_call"), "name": mustMarshalJSON("journal"),
			"call_id": mustMarshalJSON(id), "arguments": mustMarshalJSON(arguments),
		})
		if err != nil {
			t.Fatal(err)
		}
		var output map[string]json.RawMessage
		if err := json.Unmarshal([]byte(jsonString(result, "output")), &output); err != nil {
			t.Fatal(err)
		}
		return output
	}
	result := call("add", `{"op":"add","text":"Result\n\n- first\n\nParagraph.","report_now":true}`)
	if string(result["ok"]) != "true" {
		t.Fatalf("add: %s", mustMarshalJSON(result))
	}
	result = call("list", `{"op":"list"}`)
	var items []journalListItem
	if err := json.Unmarshal(result["items"], &items); err != nil || len(items) != 1 || items[0].Question != "" {
		t.Fatalf("list: %s %v", mustMarshalJSON(result), err)
	}
	body := "Result\n\n- first\n\nParagraph."
	for _, terminal := range []bool{false, true} {
		messages, err := transform.prepareJournalDelivery(terminal)
		transform.ReleaseDelivery()
		want := "Journal update `/root` (`amber`)\n" + body
		if terminal {
			want = "Journal flush `/root` (`amber`)\n\n" + body
		}
		if err != nil || len(messages) != 1 || commentaryMessageText(messages[0]) != want {
			t.Fatalf("terminal=%v: %s %v", terminal, mustMarshalJSON(messages), err)
		}
	}
	result = call("batch", `{"op":"list","journal":[{"op":"add","text":"Batched"}]}`)
	if err := json.Unmarshal(result["items"], &items); err != nil || len(items) != 2 || items[1].Question != "" {
		t.Fatalf("batch: %s %v", mustMarshalJSON(result), err)
	}
}

func TestCodeModeJournalPinsQuestionAtLowering(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	transform, _, _, _ := newMekugiTestTransformWithProxy(t, proxy)
	proxy.commentaryEndpoint = "http://localhost/internal/commentary"
	transform.journalQuestion = "Original question?"
	_, lowered, err := transform.lowerCodeModeCommentary("call", `await journal({op: "add", text: "Milestone", answer: true})`)
	if err != nil || !lowered {
		t.Fatalf("lower: %v %v", lowered, err)
	}
	transform.journalQuestion = "Later question?"
	proxy.commentary.journalPublisher = func(_ context.Context, _, _, _ string, mutations []journalMutation) ([]string, error) {
		if len(mutations) != 1 || mutations[0].Text == nil || *mutations[0].Text != "Milestone" ||
			mutations[0].Answer == nil || !*mutations[0].Answer || mutations[0].inferredQuestion != "Original question?" {
			t.Fatalf("publication source: %+v", mutations)
		}
		return []string{"amber"}, nil
	}
	request := httptest.NewRequest(http.MethodPost, commentaryPublisherPath, strings.NewReader(`{"journal":{"op":"add","text":"Milestone","answer":true},"id":"publication"}`))
	request.Header.Set("Authorization", "Bearer "+transform.commentarySubscriptions[0].token)
	writer := httptest.NewRecorder()
	proxy.commentary.serveHTTP(writer, request)
	if writer.Code != http.StatusOK {
		t.Fatalf("publish: %d %s", writer.Code, writer.Body.String())
	}
}

func TestStructuredJournalMutationIsPlainMilestone(t *testing.T) {
	transform, proxy, _, workspace := newMekugiTestTransform(t)
	transform.journalQuestion = "Run checks?"
	transform.commentaryTools = commentaryToolCatalog{
		functionToolKey("functions", "exec_command"): {qualifiedName: "functions.exec_command"},
	}
	item := map[string]json.RawMessage{
		"type": mustMarshalJSON("function_call"), "namespace": mustMarshalJSON("functions"),
		"name": mustMarshalJSON("exec_command"), "call_id": mustMarshalJSON("structured-answer"),
		"arguments": mustMarshalJSON(`{"cmd":"true","journal":[{"op":"add","text":"Passed"}]}`),
	}
	if _, err := transform.transformStructuredCommentary(item); err != nil {
		t.Fatal(err)
	}
	if jsonString(item, "arguments") != `{"cmd":"true"}` {
		t.Fatalf("host arguments: %s", item["arguments"])
	}
	items, err := proxy.journals.list(t.Context(), proxy.replayStore, workspace, "thread-1")
	if err != nil || len(items) != 1 || items[0].Question != "" {
		t.Fatalf("state: %+v %v", items, err)
	}
}

func TestJournalFlushNestsCarriageReturnLines(t *testing.T) {
	for _, newline := range []string{"\r", "\r\n"} {
		t.Run(strings.ReplaceAll(newline, "\r", "CR"), func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			transform, _, _, workspace := newMekugiTestTransformWithProxy(t, proxy)
			question := "Question?" + newline + newline + "- choice"
			body := "First" + newline + newline + "# Heading"
			mutations := bindJournalAnswers([]journalMutation{{Op: "add", Text: new(body), Answer: new(true)}}, question)
			if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "thread-1", "", mutations); err != nil {
				t.Fatal(err)
			}
			messages, err := transform.prepareJournalDelivery(true)
			transform.ReleaseDelivery()
			want := "Journal flush `/root` (`amber`)\n\n**Question:**\n\nQuestion?\n\n- choice\n\n**Answer:**\n\nFirst\n\n# Heading"
			if err != nil || len(messages) != 1 || commentaryMessageText(messages[0]) != want {
				t.Fatalf("flush: %s %v", mustMarshalJSON(messages), err)
			}
			items, err := proxy.journals.list(t.Context(), proxy.replayStore, workspace, "thread-1")
			if err != nil || len(items) != 1 || items[0].Text != body || items[0].Question != question {
				t.Fatalf("rendering changed durable content: %+v %v", items, err)
			}
		})
	}
}

func TestJournalTerminalSharedQuestions(t *testing.T) {
	for _, child := range []bool{false, true} {
		for _, flushed := range []bool{false, true} {
			name := map[bool]string{false: "main", true: "child"}[child] + "/" + map[bool]string{false: "pending", true: "first-flushed"}[flushed]
			t.Run(name, func(t *testing.T) {
				proxy := newManagedMekugiProxy(t)
				transform, _, _, workspace := newMekugiTestTransformWithProxy(t, proxy)
				defer transform.Close()
				transform.subagentTurn = child
				for _, entry := range []struct{ text, question string }{
					{"First finding", "Shared assignment?\r\n\r\n- detail"},
					{"Second finding", "Different question?"},
					{"Plain milestone", ""},
					{"Fourth finding\r\n\r\n- nested\r\n\r\n```go\r\nok()\r\n```", "Shared assignment?\r\n\r\n- detail"},
					{"Fifth finding", "Shared assignment?\r\n\r\n- detail"},
					{"Sixth finding", "Different question?"},
				} {
					mutation := journalMutation{Op: "add", Text: new(entry.text)}
					if entry.question != "" {
						mutation.Answer = new(true)
					}
					if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "thread-1", "",
						bindJournalAnswers([]journalMutation{mutation}, entry.question)); err != nil {
						t.Fatal(err)
					}
				}
				before, err := proxy.journals.list(t.Context(), proxy.replayStore, workspace, "thread-1")
				if err != nil {
					t.Fatal(err)
				}
				if flushed {
					if err := proxy.journals.acknowledge(t.Context(), proxy.replayStore, workspace, "thread-1",
						map[string]uint64{"amber": before[0].Updated}, true); err != nil {
						t.Fatal(err)
					}
					before, err = proxy.journals.list(t.Context(), proxy.replayStore, workspace, "thread-1")
					if err != nil {
						t.Fatal(err)
					}
				}
				// An abandoned delivery must not consume the question or any of its answers.
				for range 2 {
					messages, err := transform.prepareJournalDelivery(true)
					transform.ReleaseDelivery()
					if err != nil {
						t.Fatal(err)
					}
					var body string
					if child {
						body = transform.journalChildResult
						if len(messages) != 0 {
							t.Fatalf("child unexpectedly flushed: %v", messages)
						}
					} else {
						if len(messages) != 1 {
							t.Fatalf("main flush count: %d", len(messages))
						}
						body = commentaryMessageText(messages[0])
					}
					for _, question := range []string{"Shared assignment?", "Different question?"} {
						if strings.Count(body, question) != 1 {
							t.Fatalf("question %q must appear once: %s", question, body)
						}
					}
					wantGroups := [][]string{{"amber", "ash", "atlas"}, {"apple", "beach"}, {"arch"}}
					if flushed && !child {
						wantGroups = [][]string{{"apple", "beach"}, {"arch"}, {"ash", "atlas"}}
						if strings.Contains(body, "`amber`") {
							t.Fatalf("omitted item rendered: %s", body)
						}
					}
					blocks := strings.Split(body, "\n---\n")
					if len(blocks) != len(wantGroups) {
						t.Fatalf("question groups are not separate blocks: %s", body)
					}
					for index, ids := range wantGroups {
						block := blocks[index]
						last := -1
						for _, id := range ids {
							position := strings.Index(block, "\n- `"+id+"`\n")
							if position <= last {
								t.Fatalf("answers for one question are not together in order: %s", block)
							}
							last = position
						}
						if strings.Count(block, "\n- `") != len(ids) {
							t.Fatalf("unrelated answer or milestone in question block: %s", block)
						}
						question := map[string]string{"amber": "Shared assignment?", "apple": "Different question?", "ash": "Shared assignment?"}[ids[0]]
						if question == "" {
							if strings.Contains(block, "**Question:**") || strings.Contains(block, "**Answers:**") {
								t.Fatalf("plain milestone became a question block: %s", block)
							}
						} else if !strings.Contains(block, "**Question:**\n\n"+question) ||
							strings.Count(block, "**Answers:**") != 1 {
							t.Fatalf("answers grouped under the wrong question: %s", block)
						}
					}
					if strings.Contains(body, "question in `") {
						t.Fatalf("answers contain item cross-references: %s", body)
					}
					if !strings.Contains(body, "\n- `arch`\n\n  Plain milestone\n") ||
						!strings.Contains(body, "Fourth finding\n  \n  - nested\n  \n  ```go\n  ok()\n  ```") {
						t.Fatalf("plain milestone or answer Markdown changed: %s", body)
					}
				}
				after, err := proxy.journals.list(t.Context(), proxy.replayStore, workspace, "thread-1")
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatalf("rendering changed stored items: before=%+v after=%+v err=%v", before, after, err)
				}
			})
		}
	}
}
