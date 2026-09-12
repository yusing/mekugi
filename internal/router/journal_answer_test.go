package router

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	if err := apply("root", "edit", "", journalMutation{Op: "edit", ID: "j1", Text: new("Revised")}); err != nil {
		t.Fatal(err)
	}
	if err := store.initialize(t.Context(), replay, "/workspace", "fork", "/root", "root"); err != nil {
		t.Fatal(err)
	}
	if err := apply("root", "clear", "", journalMutation{Op: "edit", ID: "j1", Text: new("Plain"), Answer: new(false)}); err != nil {
		t.Fatal(err)
	}
	for thread, want := range map[string]string{"root": "", "fork": "Original?"} {
		items, err := newJournalStore().list(t.Context(), replay, "/workspace", thread)
		if err != nil || len(items) != 1 || items[0].Question != want {
			t.Fatalf("%s: %+v %v", thread, items, err)
		}
	}
	for _, question := range []string{"", " \n", "\xff", strings.Repeat("q", maxJournalItemBytes)} {
		if err := apply("root", "", question, mutation); err == nil {
			t.Fatalf("accepted invalid source of %d bytes", len(question))
		}
	}
	if err := apply("fork", "", "", journalMutation{Op: "edit", ID: "j1", Text: new(strings.Repeat("a", maxJournalItemBytes))}); err == nil {
		t.Fatal("preserved question was omitted from item budget")
	}
	if err := apply("root", "", "Next?", journalMutation{Op: "edit", ID: "j1", Text: new("New answer"), Answer: new(true)}); err != nil {
		t.Fatal(err)
	}
	items, err := store.list(t.Context(), replay, "/workspace", "root")
	if err != nil || items[0].Question != "Next?" {
		t.Fatalf("reassociation: %+v %v", items, err)
	}
	for _, answer := range []bool{true, false} {
		if err := apply("root", "", "Question", journalMutation{Op: "delete", ID: "j1", Answer: new(answer)}); err == nil {
			t.Fatal("accepted answer on delete")
		}
	}
	if _, err := decodeJournalMutations([]byte(`[{"op":"add","text":"answer","question":"invented"}]`)); err == nil {
		t.Fatal("accepted authored question")
	}
}

func TestJournalAnswerRoutingAndRendering(t *testing.T) {
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
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
	result := call("add", `{"op":"add","text":"Result\n\n- first\n\nParagraph.","answer":true,"report_now":true}`)
	if string(result["ok"]) != "true" {
		t.Fatalf("add: %s", mustMarshalJSON(result))
	}
	result = call("list", `{"op":"list"}`)
	var items []journalListItem
	if err := json.Unmarshal(result["items"], &items); err != nil || len(items) != 1 || items[0].Question != transform.journalQuestion {
		t.Fatalf("list: %s %v", mustMarshalJSON(result), err)
	}
	for _, op := range []string{"list", "finish"} {
		for _, answer := range []bool{false, true} {
			result := call(op+string(mustMarshalJSON(answer)), string(mustMarshalJSON(map[string]any{"op": op, "answer": answer})))
			if string(result["ok"]) != "false" {
				t.Fatalf("accepted %s answer: %s", op, mustMarshalJSON(result))
			}
		}
	}
	body := "**Question:**\n\nWhich?\n\n- A\n- B\n\n**Answer:**\n\nResult\n\n- first\n\nParagraph."
	for _, terminal := range []bool{false, true} {
		messages, err := transform.prepareJournalDelivery(terminal)
		transform.ReleaseDelivery()
		want := "Journal update `/root` (`j1`)\n" + body
		if terminal {
			want = "Journal flush `/root`\n- `j1`\n\n  " + strings.ReplaceAll(body, "\n", "\n  ") + "\n"
		}
		if err != nil || len(messages) != 1 || commentaryMessageText(messages[0]) != want {
			t.Fatalf("terminal=%v: %s %v", terminal, mustMarshalJSON(messages), err)
		}
	}
	result = call("batch", `{"op":"list","journal":[{"op":"add","text":"Batched","answer":true}]}`)
	if err := json.Unmarshal(result["items"], &items); err != nil || len(items) != 2 || items[1].Question != transform.journalQuestion {
		t.Fatalf("batch: %s %v", mustMarshalJSON(result), err)
	}
}

func TestCodeModeJournalPinsQuestionAtLowering(t *testing.T) {
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	transform, _, _, _ := newMekugiTestTransformWithProxy(t, proxy)
	proxy.commentaryEndpoint = "http://localhost/internal/commentary"
	transform.journalQuestion = "Original question?"
	_, lowered, err := transform.lowerCodeModeCommentary("call", `await journal({op: "add", text: "Answer", answer: true})`)
	if err != nil || !lowered {
		t.Fatalf("lower: %v %v", lowered, err)
	}
	transform.journalQuestion = "Later question?"
	proxy.commentary.journalPublisher = func(_ context.Context, _, _, _ string, mutations []journalMutation) ([]string, error) {
		if len(mutations) != 1 || mutations[0].inferredQuestion != "Original question?" || mutations[0].Answer == nil || !*mutations[0].Answer {
			t.Fatalf("publication source: %+v", mutations)
		}
		return []string{"j1"}, nil
	}
	request := httptest.NewRequest(http.MethodPost, commentaryPublisherPath, strings.NewReader(`{"journal":{"op":"add","text":"Answer","answer":true},"id":"publication"}`))
	request.Header.Set("Authorization", "Bearer "+transform.commentarySubscriptions[0].token)
	writer := httptest.NewRecorder()
	proxy.commentary.serveHTTP(writer, request)
	if writer.Code != http.StatusOK {
		t.Fatalf("publish: %d %s", writer.Code, writer.Body.String())
	}
	token := proxy.commentary.subscribeThread("session", "thread", "/root")
	proxy.commentary.bindJournalQuestion(token, "Must not attach")
	proxy.commentary.mu.Lock()
	question := proxy.commentary.routes[token].journalQuestion
	proxy.commentary.mu.Unlock()
	if question != "" {
		t.Fatal("thread-lifetime shell route accepted a request question")
	}
}

func TestStructuredJournalAnswerBindsQuestion(t *testing.T) {
	transform, proxy, _, workspace := newMekugiTestTransform(t, testTranslator(t, new(int)))
	transform.journalQuestion = "Run checks?"
	transform.commentaryTools = commentaryToolCatalog{
		functionToolKey("functions", "exec_command"): {qualifiedName: "functions.exec_command"},
	}
	item := map[string]json.RawMessage{
		"type": mustMarshalJSON("function_call"), "namespace": mustMarshalJSON("functions"),
		"name": mustMarshalJSON("exec_command"), "call_id": mustMarshalJSON("structured-answer"),
		"arguments": mustMarshalJSON(`{"cmd":"true","journal":[{"op":"add","text":"Passed","answer":true}]}`),
	}
	if _, err := transform.transformStructuredCommentary(item); err != nil {
		t.Fatal(err)
	}
	if jsonString(item, "arguments") != `{"cmd":"true"}` {
		t.Fatalf("host arguments: %s", item["arguments"])
	}
	items, err := proxy.journals.list(t.Context(), proxy.replayStore, workspace, "thread-1")
	if err != nil || len(items) != 1 || items[0].Question != "Run checks?" {
		t.Fatalf("state: %+v %v", items, err)
	}
}

func TestJournalFlushNestsCarriageReturnLines(t *testing.T) {
	for _, newline := range []string{"\r", "\r\n"} {
		t.Run(strings.ReplaceAll(newline, "\r", "CR"), func(t *testing.T) {
			proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
			transform, _, _, workspace := newMekugiTestTransformWithProxy(t, proxy)
			question := "Question?" + newline + newline + "- choice"
			body := "First" + newline + newline + "# Heading"
			mutations := bindJournalAnswers([]journalMutation{{Op: "add", Text: new(body), Answer: new(true)}}, question)
			if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "thread-1", "", mutations); err != nil {
				t.Fatal(err)
			}
			messages, err := transform.prepareJournalDelivery(true)
			transform.ReleaseDelivery()
			want := "Journal flush `/root`\n- `j1`\n\n  **Question:**\n  \n  Question?\n  \n  - choice\n  \n  **Answer:**\n  \n  First\n  \n  # Heading\n"
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
