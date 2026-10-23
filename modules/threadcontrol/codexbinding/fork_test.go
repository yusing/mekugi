package codexbinding

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"reflect"
	"testing"
)

func TestSanitizeForkProvenanceAndIdentity(t *testing.T) {
	records := forkFixture(t, false, `
{"timestamp":"2026-10-23T00:00:00Z","type":"session_meta","payload":{"id":"source-thread","cwd":"/source/work","forked_from_id":"root-thread","source":"cli"}}
{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"generated role"},{"type":"input_text","text":"custom role lookalike"},{"type":"input_image","image_url":"data:image/png;base64,AA=="},{"type":"input_text","text":"generated mode"},{"type":"input_text","text":"approved action"},{"type":"input_text","text":"current time"},{"type":"input_text","text":"time unavailable"},{"type":"input_text","text":"usage"},{"type":"input_text","text":"ordinary policy"}],"internal_chat_message_metadata_passthrough":{"content_item_kinds":["multi_agent.role_instructions",null,null,"multi_agent.mode_instructions","guardian.approved_action","current_time.reminder","current_time.unavailable","multi_agent.usage_hint","managed_config.developer_instructions"],"other":"retained"}}}
{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"generated role"},{"type":"input_text","text":"current time"}]}}
{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"generated role"}],"internal_chat_message_metadata_passthrough":{"content_item_kinds":["multi_agent.role_instructions"]}}}
`)
	if len(records) != 3 {
		t.Fatalf("got %d records, want header and two developer messages", len(records))
	}
	assertForkJSON(t, records[0]["payload"], `{"id":"source-thread","cwd":"/source/work","forked_from_id":"root-thread","source":"cli"}`)
	mixed := forkDecodeObject(t, records[1]["payload"])
	assertForkJSON(t, mixed["content"], `[{"type":"input_text","text":"custom role lookalike"},{"type":"input_image","image_url":"data:image/png;base64,AA=="},{"type":"input_text","text":"ordinary policy"}]`)
	assertForkJSON(t, mixed["internal_chat_message_metadata_passthrough"], `{"content_item_kinds":[null,null,"managed_config.developer_instructions"],"other":"retained"}`)
	custom := forkDecodeObject(t, records[2]["payload"])
	assertForkJSON(t, custom["content"], `[{"type":"input_text","text":"generated role"},{"type":"input_text","text":"current time"}]`)
}

func TestSanitizeForkInheritedMessages(t *testing.T) {
	records := forkFixture(t, false, `
{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"human input"}]},"metadata":{"user_input_order":7,"source":"composer"}}
{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"attributed message"}]},"metadata":{"user_input_order":8,"sender_user_messages":[{"sender":"parent"}]}}
{"type":"response_item","payload":{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"answer"}]},"metadata":{"user_input_order":9,"sender_user_messages":null}}
{"type":"response_item","payload":{"type":"message","role":"assistant","phase":"partial_answer","content":[{"type":"output_text","text":"partial"}]}}
{"type":"response_item","payload":{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"working"}]}}
{"type":"response_item","payload":{"type":"function_call","call_id":"call-1","name":"exec_command","arguments":"{}"}}
{"type":"response_item","payload":{"type":"function_call_output","call_id":"call-1","output":"done"}}
`)
	if len(records) != 4 {
		t.Fatalf("got %d records, want four inherited messages", len(records))
	}
	assertForkJSON(t, records[0]["metadata"], `{"user_input_order":7,"source":"composer","inherited_user_message":true}`)
	for _, record := range records[1:] {
		assertForkJSON(t, record["metadata"], `{"inherited_user_message":true}`)
	}
}

func TestSanitizeForkPreservePrefix(t *testing.T) {
	records := forkFixture(t, true, `
{"type":"response_item","payload":{"type":"function_call","call_id":"call-1","name":"exec_command","arguments":"{\"cmd\":\"pwd\"}"},"metadata":{"user_input_order":2}}
{"type":"response_item","payload":{"type":"function_call_output","call_id":"call-1","output":"/source/work"}}
{"type":"response_item","payload":{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"working"}]}}
`)
	if len(records) != 3 {
		t.Fatalf("got %d records, want preserved call, output and commentary", len(records))
	}
	assertForkJSON(t, records[0]["payload"], `{"type":"function_call","call_id":"call-1","name":"exec_command","arguments":"{\"cmd\":\"pwd\"}"}`)
	assertForkJSON(t, records[1]["payload"], `{"type":"function_call_output","call_id":"call-1","output":"/source/work"}`)
	for _, record := range records {
		assertForkJSON(t, record["metadata"], `{"inherited_user_message":true}`)
	}
}

func TestSanitizeForkCompaction(t *testing.T) {
	records := forkFixture(t, false, `
{"type":"retained_context","payload":{"verified_answers":[{"action":"write"}],"incomplete":false,"root_thread_id":"root-thread","user_context":"keep"}}
{"type":"world_state","payload":{"state":{"multi_agent_usage_hint":"parent hint","user_state":"keep"}}}
{"type":"compacted","payload":{"message":"summary","latest_token_usage_record":{"total":100},"guardian_history":[{"approved":true}],"retained_context":{"verified_answers":[{"action":"write"}],"incomplete":true,"root_thread_id":"root-thread","user_context":"keep"},"resume_metadata":{"multi_agent_version":"v1","turn_attribution":{"thread":"parent"},"previous_turn_settings":{"model":"source-model"}},"replacement_history":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"generated role"}],"internal_chat_message_metadata_passthrough":{"content_item_kinds":["multi_agent.role_instructions"]}},{"type":"message","role":"user","content":[{"type":"input_text","text":"question"}]},{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"answer"}]}],"replacement_history_metadata":[{"marker":"discard"},{"marker":"question","user_input_order":4,"sender_user_messages":[{"sender":"parent"}]},{"marker":"answer","user_input_order":5}]}}
`)
	assertForkJSON(t, records[0]["payload"], `{"verified_answers":[],"incomplete":true,"root_thread_id":"root-thread","user_context":"keep"}`)
	assertForkJSON(t, records[1]["payload"], `{"state":{"user_state":"keep"}}`)
	compact := forkDecodeObject(t, records[2]["payload"])
	for _, key := range []string{"latest_token_usage_record", "guardian_history"} {
		if _, ok := compact[key]; ok {
			t.Errorf("compaction retained local %s", key)
		}
	}
	assertForkJSON(t, compact["retained_context"], `{"verified_answers":[],"incomplete":true,"root_thread_id":"root-thread","user_context":"keep"}`)
	assertForkJSON(t, compact["resume_metadata"], `{"multi_agent_version":"v2","previous_turn_settings":{"model":"source-model"}}`)
	assertForkJSON(t, compact["replacement_history"], `[{"type":"message","role":"user","content":[{"type":"input_text","text":"question"}]},{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"answer"}]}]`)
	assertForkJSON(t, compact["replacement_history_metadata"], `[{"marker":"question","inherited_user_message":true},{"marker":"answer","inherited_user_message":true}]`)
}

func TestSanitizeForkLegacyCompaction(t *testing.T) {
	for _, history := range []string{"", `,"replacement_history":null`} {
		t.Run(history, func(t *testing.T) {
			records := forkFixture(t, true, `
{"type":"turn_context","payload":{"model":"source-model"}}
{"type":"world_state","payload":{"state":{"multi_agent_usage_hint":"parent"}}}
{"type":"response_item","payload":{"type":"additional_tools","tools":[]}}
{"type":"response_item","payload":{"type":"function_call","call_id":"call-1","name":"exec_command","arguments":"{}"}}
{"type":"response_item","payload":{"type":"function_call_output","call_id":"call-1","output":"done"}}
{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"model base"},{"type":"input_text","text":"tool baseline"},{"type":"input_text","text":"managed config"},{"type":"input_text","text":"model instructions"},{"type":"input_text","text":"persistent mode"},{"type":"input_text","text":"custom"}],"internal_chat_message_metadata_passthrough":{"content_item_kinds":["model.base_instructions","tools.incremental_update","managed_config.developer_instructions","model.instructions","persistent_mode.instructions",null]}}}
{"type":"compacted","payload":{"message":"legacy summary","resume_metadata":{"previous_turn_settings":{"model":"source-model"},"turn_attribution":{"thread":"parent"}}`+history+`}}
`)
			if len(records) != 2 {
				t.Fatalf("got %d records, want custom developer message and compaction", len(records))
			}
			developer := forkDecodeObject(t, records[0]["payload"])
			assertForkJSON(t, developer["content"], `[{"type":"input_text","text":"custom"}]`)
			assertForkJSON(t, developer["internal_chat_message_metadata_passthrough"], `{"content_item_kinds":[null]}`)
			compact := forkDecodeObject(t, records[1]["payload"])
			assertForkJSON(t, compact["resume_metadata"], `{"multi_agent_version":"v2"}`)
		})
	}
}

func TestSanitizeForkRejectsMismatchedMetadata(t *testing.T) {
	for name, source := range map[string]string{
		"content indices":     `{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"policy"}],"internal_chat_message_metadata_passthrough":{"content_item_kinds":[]}}}`,
		"replacement indices": `{"type":"compacted","payload":{"replacement_history":[{"type":"message","role":"user","content":[{"type":"input_text","text":"question"}]}],"replacement_history_metadata":[]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			input := []byte(source)
			original := bytes.Clone(input)
			if output, err := sanitizeFork(input, false); err == nil {
				t.Fatalf("accepted mismatched metadata: %s", output)
			}
			if !bytes.Equal(input, original) {
				t.Fatal("sanitizer changed source bytes on rejection")
			}
		})
	}
}

func forkFixture(t *testing.T, preserve bool, source string) []forkObject {
	t.Helper()
	input := []byte(source)
	original := bytes.Clone(input)
	output, err := sanitizeFork(input, preserve)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(input, original) {
		t.Fatal("sanitizer changed source bytes")
	}
	var records []forkObject
	for line := range bytes.SplitSeq(bytes.TrimSpace(output), []byte{'\n'}) {
		records = append(records, forkDecodeObject(t, line))
	}
	return records
}

func forkDecodeObject(t *testing.T, data []byte) forkObject {
	t.Helper()
	var object forkObject
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatal(err)
	}
	return object
}

func assertForkJSON(t *testing.T, actual jsontext.Value, expected string) {
	t.Helper()
	var got, want any
	if err := json.Unmarshal(actual, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(expected), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %s, want %s", actual, expected)
	}
}
