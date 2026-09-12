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
			if got := journalQuestionFromInput(mustTestJSON(t, test.input)); got != test.want {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
}

func TestJournalAnswerSourceIsRequestLocal(t *testing.T) {
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
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
