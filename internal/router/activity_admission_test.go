package router

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func activityAdmissionRequest(t *testing.T, input []any) parsedResponsesRequest {
	t.Helper()
	request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{
		"model": "gpt-test", "input": append([]any{testCodeModeAdditionalTools(testCodeModeDescription)}, input...),
	}))
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func TestFailedPreparationDoesNotChangeActivityIdentity(t *testing.T) {
	t.Parallel()
	proxy := newManagedMekugiProxy(t)
	root, _ := prepareActivityTest(t, proxy, "root-session", "r", "", "/root", nil)
	child, _ := prepareActivityTest(t, proxy, "child-session", "c", "r", "/root/child", nil)
	child.Close()
	root.drainActivity()
	request, err := parseResponsesRequest([]byte(`{"model":"gpt-test","input":[],"tools":[{"type":"function","name":"apply_patch"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = proxy.prepareRequest(t.Context(), &request, "bad-session", "c", codexTurnMetadata{
		RequestKind: "turn", ThreadID: "c", ParentThreadID: "other-root", AgentName: "/root/other", SubagentKind: "thread_spawn",
	}, true)
	if err == nil || !strings.Contains(err.Error(), "Native apply_patch must be a custom tool") {
		t.Fatal("expected invalid catalog", err)
	}
	next, _ := prepareActivityTest(t, proxy, "next-session", "c", "r", "/root/child", nil)
	next.collectProviderCommentary(assistantCommentaryMessage("progress", "Still working."))
	messages := root.drainActivity()
	if len(messages) != 1 || !strings.Contains(commentaryText(t, messages[0]), "[`/root/child`] Still working.") {
		t.Fatal("failed request poisoned later activity", messages)
	}
}

func TestRejectedIdentityCannotReuseShellActivityAncestry(t *testing.T) {
	t.Parallel()
	for _, identity := range []string{
		`"thread_id":"different","parent_thread_id":"r","agent_name":"/root/child"`,
		`"thread_id":"c","parent_thread_id":42,"agent_name":"/root/child"`,
	} {
		t.Run(identity, func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			proxy.commentaryEndpoint = "http://127.0.0.1:8080" + commentaryPublisherPath
			root, _ := prepareActivityTest(t, proxy, "root-session", "r", "", "/root", nil)
			child, _ := prepareActivityTest(t, proxy, "child-session", "c", "r", "/root/child", nil)
			token := proxy.commentary.subscribeThread(child.historySessionID, "c", "/root/child")
			root.drainActivity()
			metadata, valid := decodeCodexTurnMetadata(http.Header{codexTurnMetadataHeader: []string{
				`{"request_kind":"turn","subagent_kind":"thread_spawn",` + identity + `}`,
			}})
			request := activityAdmissionRequest(t, nil)
			next, err := proxy.prepareRequest(t.Context(), &request, "next-session", "c", metadata, valid)
			if err != nil {
				t.Fatal("auxiliary identity rejected tool execution", err)
			}
			t.Cleanup(next.Close)
			if !proxy.commentary.publish(token, "Shell progress.", false) {
				t.Fatal("existing shell capability was retired")
			}
			if got := root.drainActivity(); len(got) != 0 {
				t.Fatal("shell reused rejected ancestry", got)
			}
			local := proxy.commentary.drain(token)
			if len(local) != 1 || local[0].text != "[`/root/child`] Shell progress." {
				t.Fatal("immutable local runtime provenance changed", local)
			}
			// Shared workers cannot be assigned to individual requests. A later
			// valid turn must not re-enable delayed work from the rejected identity.
			_, _ = prepareActivityTest(t, proxy, "later-session", "c", "r", "/root/child", nil)
			proxy.commentary.publish(token, "Delayed shell progress.", false)
			if got := root.drainActivity(); len(got) != 0 {
				t.Fatal("later turn restored ambiguous ancestry", got)
			}
		})
	}
}

func TestReplyRecipientRequiresCurrentValidIdentity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, author, kind, recipient string
		invalid, want                 bool
	}{
		{name: "unnamed child", kind: "thread_spawn"},
		{name: "invalid child", author: "not-canonical", kind: "thread_spawn", recipient: "not-canonical"},
		{name: "malformed child metadata", author: "/root/child", kind: "thread_spawn", recipient: "/root/child", invalid: true},
		{name: "root with child name", author: "/root/child", recipient: "/root", want: true},
		{name: "root with malformed auxiliary identity", recipient: "/root", invalid: true, want: true},
		{name: "root ignores child recipient", author: "/root/child", recipient: "/root/child"},
		{name: "valid child", author: "/root/child", kind: "thread_spawn", recipient: "/root/child", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			envelope := map[string]any{"type": "agent_message", "id": "received", "author": "/root/sender",
				"content": []any{map[string]any{"type": "input_text", "text": "Message Type: MESSAGE\nSender: /root/sender\nPayload:\nReply payload."}}}
			if tc.recipient != "" {
				envelope["recipient"] = tc.recipient
			}
			request := activityAdmissionRequest(t, []any{envelope})
			metadata := codexTurnMetadata{RequestKind: "turn", ThreadID: "c", AgentName: tc.author, SubagentKind: tc.kind, activityIdentityInvalid: tc.invalid}
			if tc.kind != "" {
				metadata.ParentThreadID = "r"
			}
			transform, err := proxy.prepareRequest(t.Context(), &request, "session", "c", metadata, true)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(transform.Close)
			output, err := transform.TransformJSON([]byte(`{"status":"completed","output":[]}`))
			if err != nil || bytes.Contains(output, []byte("Reply payload.")) != tc.want {
				t.Fatal("incorrect reply projection", string(output), err)
			}
			if !bytes.Contains(request.fields["input"], mustTestJSON(t, envelope)) {
				t.Fatal("original envelope changed")
			}
		})
	}
}

func TestInitiallyAmbiguousShellActivityCannotAcquireAncestry(t *testing.T) {
	for _, tc := range []struct {
		name, author string
		invalid      bool
	}{
		{name: "malformed wire identity", author: "/root/child", invalid: true},
		{name: "noncanonical string identity", author: "not-canonical"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			proxy.commentaryEndpoint = "http://127.0.0.1:8080" + commentaryPublisherPath
			root, _ := prepareActivityTest(t, proxy, "root-session", "r", "", "/root", nil)
			request := activityAdmissionRequest(t, nil)
			child, err := proxy.prepareRequest(t.Context(), &request, "child-session", "c", codexTurnMetadata{
				RequestKind: "turn", ParentThreadID: "r", AgentName: tc.author, SubagentKind: "thread_spawn", activityIdentityInvalid: tc.invalid,
			}, true)
			if err != nil {
				t.Fatal(err)
			}
			if child == nil {
				return // Invalid auxiliary identity cannot acquire a runtime capability.
			}
			t.Cleanup(child.Close)
			token := proxy.commentary.subscribeThread(child.historySessionID, "c", "/root/child")
			_, _ = prepareActivityTest(t, proxy, "later-session", "c", "r", "/root/child", nil)
			if !proxy.commentary.publish(token, "Delayed ambiguous work.", false) {
				t.Fatal("local shell route was retired")
			}
			if got := root.drainActivity(); len(got) != 0 {
				t.Fatal("ambiguous capability acquired later ancestry", got)
			}
		})
	}
}

func TestUnnamedRecipientPreservesUnretainedPrefixMessage(t *testing.T) {
	generated := assistantCommentaryMessage(subagentCommentaryMessageID("old"), "Old notice.")
	envelope := map[string]any{"type": "agent_message", "author": "/root/sender", "content": []any{map[string]any{"type": "encrypted_content"}}}
	fields := map[string]json.RawMessage{"input": mustTestJSON(t, []any{generated, envelope})}
	if got := prepareSubagentInputEnvelopes(fields, "").commentary; len(got) != 0 {
		t.Fatal("absent recipient matched absent identity", got)
	}
	if !bytes.Equal(fields["input"], mustTestJSON(t, []any{generated, envelope})) {
		t.Fatal("unretained prefix-matching message or original envelope was removed")
	}
}
