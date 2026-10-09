package claude

import (
	"fmt"
	"testing"

	"github.com/yusing/mekugi/internal/session"
)

func TestAdapterNativeCurrentContextSnapshots(t *testing.T) {
	var a adapter
	for _, tc := range []struct {
		frame string
		want  *uint64
	}{
		{`{"type":"stream_event","event":{"type":"message_start","message":{"id":"m","usage":{"input_tokens":50,"cache_read_input_tokens":100,"output_tokens":1}}}}`, new(uint64(151))},
		{`{"type":"stream_event","event":{"type":"message_delta","usage":{"output_tokens":9}}}`, new(uint64(159))},
		{`{"type":"stream_event","event":{"type":"message_start","message":{"id":"next","usage":{"input_tokens":10,"output_tokens":0}}}}`, new(uint64(10))},
		{`{"type":"stream_event","event":{"type":"message_delta","usage":{"input_tokens":200000,"output_tokens":20}}}`, nil},
		{`{"type":"stream_event","event":{"type":"message_delta","usage":{"input_tokens":200000,"output_tokens":20,"iterations":[{"type":"message","input_tokens":149998,"output_tokens":1},{"type":"message","input_tokens":10,"cache_read_input_tokens":5,"output_tokens":2},{"type":"advisor_message","input_tokens":900000,"output_tokens":99}]}}}`, new(uint64(17))},
		{`{"type":"stream_event","event":{"type":"message_start","message":{"id":"unknown"}}}`, nil},
	} {
		frame := fmt.Sprintf(`{"kind":"event","event":%s}`, tc.frame[:len(tc.frame)-1]+`,"session_id":"main"}`)
		assertDecode(t, &a, frame, []session.Event{{Kind: "context_usage", SessionID: "main", ContextTokens: tc.want}})
	}
	for _, tc := range []struct {
		usage string
		want  *uint64
	}{
		{`{"input_tokens":0,"output_tokens":0,"iterations":[]}`, new(uint64(0))},
		{`{"input_tokens":200000,"output_tokens":20,"iterations":[{"type":"message","input_tokens":10,"cache_creation_input_tokens":5,"output_tokens":2}]}`, new(uint64(17))},
		{`{"input_tokens":1,"output_tokens":1,"iterations":[{"type":"message"}]}`, nil},
		{`{"input_tokens":-1,"output_tokens":1}`, nil},
		{`{"input_tokens":18446744073709551615,"output_tokens":1}`, nil},
	} {
		assertDecode(t, &a, `{"kind":"context_usage","sessionID":"resumed","usage":`+tc.usage+`}`, []session.Event{{Kind: "context_usage", SessionID: "resumed", ContextTokens: tc.want}})
	}
	assertDecode(t, &a, `{"kind":"event","event":{"session_id":"main","parent_tool_use_id":"child","type":"stream_event","event":{"type":"message_start","message":{"id":"child","usage":{"input_tokens":900000,"output_tokens":1}}}}}`, nil)
}
