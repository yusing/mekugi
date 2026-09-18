package router

import (
	"encoding/json"
	"testing"
)

func TestJournalQuestionFromVisibleInput(t *testing.T) {
	message := func(text string) map[string]any {
		return map[string]any{"type": "message", "role": "user", "content": text}
	}
	old := message("Earlier question?")
	latest := message("Do flags have to come before IDs?")
	for _, test := range []struct {
		name  string
		input any
		want  string
	}{
		{"string input", "Why?", "Why?"},
		{"latest message", []any{old, latest}, "Do flags have to come before IDs?"},
		{"legacy context", []any{latest, message("<environment_context>\n<cwd>/w</cwd>\n</environment_context>"), message("# AGENTS.md instructions for /w\n<INSTRUCTIONS>rules</INSTRUCTIONS>")}, "Do flags have to come before IDs?"},
		{"non-user", []any{latest, map[string]any{"role": "developer", "content": "instructions"}, map[string]any{"role": "assistant", "content": "answer"}}, "Do flags have to come before IDs?"},
		{"media only clears source", []any{old, map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_image", "image_url": "image"}}}}, ""},
		{"text blocks", []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Why?"}, map[string]any{"type": "input_text", "text": "Please explain."}}}}, "Why?\nPlease explain."},
		{"classified context", []any{latest, map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Host metadata"}}, "internal_chat_message_metadata_passthrough": map[string]any{"content_item_kinds": []string{"agents_md.instructions"}}}}, "Do flags have to come before IDs?"},
		{"classified user quote", []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "<turn_aborted>quoted</turn_aborted>"}}, "internal_chat_message_metadata_passthrough": map[string]any{"content_item_kinds": []string{"user.text"}}}}, "<turn_aborted>quoted</turn_aborted>"},
		{"editor prefix", []any{message("Open files: x.go\n## My request for Codex:\nWhy does this fail?")}, "Why does this fail?"},
		{"subagent context", []any{latest, message("<subagent_notification>Complete</subagent_notification>")}, "Do flags have to come before IDs?"},
		{"unsupported media clears source", []any{old, map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Image unavailable"}}, "internal_chat_message_metadata_passthrough": map[string]any{"content_item_kinds": []string{"images.unsupported"}}}}, ""},
		{"no inherited source", []any{map[string]any{"role": "assistant", "content": "summary"}}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := journalQuestionFromInput(mustTestJSON(t, test.input), ""); got != test.want {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
}

func TestJournalAnswerSourceIsRequestLocal(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	for _, question := range []string{"Original question?", "Steered question?"} {
		request := serverRequest(t, func(fields map[string]any) {
			fields["input"] = []any{testFlatCodeModeAdditionalTools(testCodeModeDescription), map[string]any{"role": "user", "content": question}}
		})
		transform, err := proxy.prepareRequest(t.Context(), &request, "session", "thread", codexTurnMetadata{RequestKind: "turn"}, true)
		if err != nil || transform == nil {
			t.Fatalf("prepare request: %v", err)
		}
		defer transform.Close()
		if transform.journalQuestion != question {
			t.Fatalf("source = %q, want %q", transform.journalQuestion, question)
		}
		result, err := transform.executeJournalCall(map[string]json.RawMessage{
			"type": mustTestJSON(t, "function_call"), "name": mustTestJSON(t, "journal"),
			"call_id": mustTestJSON(t, question), "arguments": mustTestJSON(t, `{"op":"add","answer":true,"text":"No."}`),
		})
		if err != nil || jsonString(result, "output") == "" {
			t.Fatalf("answer call: %s %v", result, err)
		}
	}
	items, err := proxy.journals.list(t.Context(), proxy.replayStore, "", "thread")
	if err != nil || len(items) != 2 || items[0].Question != "Original question?" || items[1].Question != "Steered question?" {
		t.Fatalf("question association: %+v %v", items, err)
	}
}

func journalTestAssignment(recipient, kind, payload string) map[string]any {
	return map[string]any{
		"type": "agent_message", "author": "/root", "recipient": recipient,
		"content": []any{map[string]any{"type": "input_text",
			"text": "Message Type: " + kind + "\nTask name: " + recipient + "\nSender: /root\nPayload:\n" + payload}},
	}
}

func TestJournalNativeAssignmentSource(t *testing.T) {
	user := map[string]any{"role": "user", "content": "User question"}
	task := journalTestAssignment("/root/child", "NEW_TASK", "Assignment\n\n- details")
	encrypted := journalTestAssignment("/root/child", "NEW_TASK", "")
	encrypted["content"] = append(encrypted["content"].([]any), map[string]any{"type": "encrypted_content", "encrypted_content": "opaque"})
	multipart := journalTestAssignment("/root/child", "NEW_TASK", "First part")
	multipart["content"] = append(multipart["content"].([]any), map[string]any{"type": "input_text", "text": "Second part"})
	wrongSender := journalTestAssignment("/root/child", "NEW_TASK", "Wrong sender")
	wrongSender["author"] = "/root/other"
	wrongRecipient := journalTestAssignment("/root/other", "NEW_TASK", "Wrong recipient")
	wrongRecipient["recipient"] = "/root/child"
	quoted := map[string]any{"role": "assistant", "content": task["content"]}
	for _, test := range []struct {
		name      string
		recipient string
		input     []any
		want      string
	}{
		{"fresh assignment", "/root/child", []any{task}, "Assignment\n\n- details"},
		{"assignment supersedes inherited user", "/root/child", []any{user, task}, "Assignment\n\n- details"},
		{"later user wins", "/root/child", []any{task, user}, "User question"},
		{"followup wins", "/root/child", []any{task, journalTestAssignment("/root/child", "NEW_TASK", "Followup")}, "Followup"},
		{"message does not override", "/root/child", []any{task, journalTestAssignment("/root/child", "MESSAGE", "Status")}, "Assignment\n\n- details"},
		{"completion does not override", "/root/child", []any{task, journalTestAssignment("/root/child", "FINAL_ANSWER", "Done")}, "Assignment\n\n- details"},
		{"other recipient", "/root/child", []any{user, journalTestAssignment("/root/other", "NEW_TASK", "Other")}, "User question"},
		{"wrong sender header", "/root/child", []any{user, wrongSender}, "User question"},
		{"wrong recipient header", "/root/child", []any{user, wrongRecipient}, "User question"},
		{"unknown current identity", "", []any{task}, ""},
		{"main does not use tasks", "/root", []any{user, journalTestAssignment("/root", "NEW_TASK", "Not a child")}, "User question"},
		{"assistant quote is not native", "/root/child", []any{user, quoted}, "User question"},
		{"encrypted blocks stale source", "/root/child", []any{user, task, encrypted}, ""},
		{"empty blocks stale source", "/root/child", []any{task, journalTestAssignment("/root/child", "NEW_TASK", "")}, ""},
		{"multipart plaintext", "/root/child", []any{multipart}, "First part\nSecond part"},
		{"payload stays exact", "/root/child", []any{journalTestAssignment("/root/child", "NEW_TASK", "  ## My request for Codex:\nTask\n")}, "  ## My request for Codex:\nTask\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := journalQuestionFromInput(mustTestJSON(t, test.input), test.recipient); got != test.want {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
}

func TestJournalAssignmentAnswerAfterRestartAndFollowup(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	var err error
	proxy.replayStore, err = openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var history []any
	for index, question := range []string{"Initial assignment", "Followup assignment"} {
		history = append(history, journalTestAssignment("/root/child", "NEW_TASK", question))
		child, _ := prepareActivityTest(t, proxy, question, "child", "root", "/root/child", history)
		if child.journalQuestion != question {
			t.Fatalf("request source = %q, want %q", child.journalQuestion, question)
		}
		result, err := child.executeJournalCall(map[string]json.RawMessage{
			"type": mustTestJSON(t, "function_call"), "name": mustTestJSON(t, "journal"),
			"call_id":   mustTestJSON(t, question),
			"arguments": mustTestJSON(t, `{"op":"finish","journal":[{"op":"add","text":"Completed","answer":true}]}`),
		})
		if err != nil || !child.journalTerminalReady() {
			t.Fatalf("assignment finish failed: %s, %v", mustTestJSON(t, result), err)
		}
		items, err := proxy.journals.list(t.Context(), proxy.replayStore, child.directory, "child")
		if err != nil || len(items) != index+1 || items[index].Question != question || items[0].Question != "Initial assignment" {
			t.Fatalf("assignment association lost: %+v, %v", items, err)
		}
		child.Close()
		proxy.journals = newJournalStore()
		proxy.activity = newSubagentActivity()
		proxy.replayStore, err = openMekugiReplayStore(proxy.replayStore.directory)
		if err != nil {
			t.Fatal(err)
		}
	}
}
