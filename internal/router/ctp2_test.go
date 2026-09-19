package router

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/tokenizer"
)

func TestCTP2RequestUsesContentLocalDictionaries(t *testing.T) {
	codec := mustCTP2Codec(t)
	repeated := strings.Repeat("alpha beta gamma delta epsilon; ", 12)
	request := parsedResponsesRequest{fields: map[string]json.RawMessage{
		"instructions": mustTestJSON(t, "decode CTP/2 here"),
		"input": mustTestJSON(t, []any{
			map[string]any{"type": "message", "role": "user", "content": repeated},
			map[string]any{"type": "custom_tool_call", "call_id": "call", "name": "exec", "input": repeated},
			map[string]any{"type": "function_call", "call_id": "fn", "name": "run", "arguments": repeated},
		}),
		"tools": mustTestJSON(t, []any{map[string]any{
			"type": "function", "name": "run", "description": repeated,
			"parameters": map[string]any{"type": "string", "description": "!V schema text stays native"},
		}}),
	}}

	transform, _, err := prepareCTP2TestRequest(t, codec, &request)
	if err != nil {
		t.Fatal(err)
	}
	if transform == nil {
		t.Fatal("request was not transformed")
	}
	if got := jsonString(request.fields, "instructions"); got != "decode CTP/2 here" {
		t.Fatalf("instructions = %q", got)
	}

	input := decodeCTP2TestValue(t, request.fields["input"]).([]any)
	for index, field := range []string{"content", "input", "arguments"} {
		object := input[index].(map[string]any)
		compact := object[field].(string)
		if !strings.HasPrefix(compact, ctp2DictionaryTag) {
			t.Fatalf("input %d compact value = %q", index, compact)
		}
		decoded, err := decodeCTP2String(compact, transform.sources, upstreamJSONBufferBytes)
		if err != nil || decoded != repeated {
			t.Fatalf("input %d decoded = %q, err = %v", index, decoded, err)
		}
	}
	tools := decodeCTP2TestValue(t, request.fields["tools"]).([]any)
	tool := tools[0].(map[string]any)
	description := tool["description"].(string)
	decoded, err := decodeCTP2String(description, transform.sources, upstreamJSONBufferBytes)
	if err != nil || decoded != repeated {
		t.Fatalf("tool description decoded = %q, err = %v", decoded, err)
	}
	parameters := tool["parameters"].(map[string]any)
	if got := parameters["description"]; got != "!V schema text stays native" {
		t.Fatalf("schema description = %q", got)
	}
}

func TestCTP2RequestPreservesProviderOwnedInputFields(t *testing.T) {
	codec := mustCTP2Codec(t)
	repeated := strings.Repeat("preserve provider-owned input data; ", 16)
	request := parsedResponsesRequest{fields: map[string]json.RawMessage{
		"instructions": mustTestJSON(t, "decode CTP/2 here"),
		"input": mustTestJSON(t, []any{
			map[string]any{
				"type": "message", "role": "user", "call_id": 42,
				"provider_item": map[string]any{"kept": true},
				"content": []any{
					map[string]any{
						"type": "input_text", "text": repeated,
						"provider_part": map[string]any{"kept": true},
					},
					map[string]any{"type": "input_image", "image_url": "provider://image", "provider_part": true},
				},
			},
			map[string]any{"type": "future_item", "content": repeated, "provider_item": true},
		}),
	}}

	transform, _, err := prepareCTP2TestRequest(t, codec, &request)
	if err != nil || transform == nil {
		t.Fatalf("transform = %#v, err = %v", transform, err)
	}
	var input []struct {
		Type         string          `json:"type"`
		Content      json.RawMessage `json:"content"`
		ProviderItem json.RawMessage `json:"provider_item"`
	}
	if err := json.Unmarshal(request.fields["input"], &input); err != nil {
		t.Fatal(err)
	}
	if len(input) != 2 || string(input[0].ProviderItem) != `{"kept":true}` || string(input[1].ProviderItem) != "true" {
		t.Fatalf("provider-owned items changed: %s", request.fields["input"])
	}
	var content []struct {
		Type         string          `json:"type"`
		Text         string          `json:"text"`
		ImageURL     string          `json:"image_url"`
		ProviderPart json.RawMessage `json:"provider_part"`
	}
	if err := json.Unmarshal(input[0].Content, &content); err != nil {
		t.Fatal(err)
	}
	if len(content) != 2 || string(content[0].ProviderPart) != `{"kept":true}` ||
		content[1].ImageURL != "provider://image" || string(content[1].ProviderPart) != "true" {
		t.Fatalf("provider-owned content changed: %s", input[0].Content)
	}
	decoded, err := decodeCTP2String(content[0].Text, transform.sources, upstreamJSONBufferBytes)
	if err != nil || decoded != repeated {
		t.Fatalf("text decoded = %q, err = %v", decoded, err)
	}
	var futureContent string
	if err := json.Unmarshal(input[1].Content, &futureContent); err != nil || futureContent != repeated {
		t.Fatalf("future item content = %q, err = %v", futureContent, err)
	}
}

func TestCTP2RequestUsesCatalogForAdditionalToolDescriptions(t *testing.T) {
	codec := mustCTP2Codec(t)
	repeated := strings.Repeat("shared additional tool description; ", 16)
	request := parsedResponsesRequest{fields: map[string]json.RawMessage{
		"instructions": mustTestJSON(t, "decode CTP/2 here"),
		"input": mustTestJSON(t, []any{map[string]any{
			"type": "additional_tools",
			"tools": []any{
				map[string]any{"type": "custom", "name": "exec", "description": repeated},
				map[string]any{
					"type": "namespace", "name": "functions", "description": repeated,
					"tools": []any{map[string]any{"type": "function", "name": "lookup", "description": repeated}},
				},
			},
		}}),
	}}

	transform, _, err := prepareCTP2TestRequest(t, codec, &request)
	if err != nil || transform == nil {
		t.Fatalf("transform = %#v, err = %v", transform, err)
	}
	input := decodeCTP2TestValue(t, request.fields["input"]).([]any)
	tools := input[0].(map[string]any)["tools"].([]any)
	descriptions := []string{
		tools[0].(map[string]any)["description"].(string),
		tools[1].(map[string]any)["description"].(string),
		tools[1].(map[string]any)["tools"].([]any)[0].(map[string]any)["description"].(string),
	}
	for index, description := range descriptions {
		decoded, decodeErr := decodeCTP2String(description, transform.sources, upstreamJSONBufferBytes)
		if decodeErr != nil || decoded != repeated {
			t.Fatalf("description %d decoded = %q, err = %v", index, decoded, decodeErr)
		}
	}
}

func TestCTP2MalformedAdditionalToolsDoNotDisableOtherEncoding(t *testing.T) {
	codec := mustCTP2Codec(t)
	repeated := strings.Repeat("compress the unaffected message; ", 16)
	request := parsedResponsesRequest{fields: map[string]json.RawMessage{
		"instructions": mustTestJSON(t, "decode CTP/2 here"),
		"input": mustTestJSON(t, []any{
			map[string]any{"type": "additional_tools"},
			map[string]any{"type": "message", "role": "user", "content": repeated},
		}),
	}}

	transform, _, err := prepareCTP2TestRequest(t, codec, &request)
	if err != nil || transform == nil {
		t.Fatalf("transform = %#v, err = %v", transform, err)
	}
	input := decodeCTP2TestValue(t, request.fields["input"]).([]any)
	if _, exists := input[0].(map[string]any)["tools"]; exists {
		t.Fatal("missing additional tools field was added")
	}
	compact := input[1].(map[string]any)["content"].(string)
	decoded, decodeErr := decodeCTP2String(compact, transform.sources, upstreamJSONBufferBytes)
	if decodeErr != nil || decoded != repeated {
		t.Fatalf("decoded message = %q, err = %v", decoded, decodeErr)
	}
}

func TestCTP2PreservesNonStringToolDescriptions(t *testing.T) {
	codec := mustCTP2Codec(t)
	request := parsedResponsesRequest{fields: map[string]json.RawMessage{
		"instructions": mustTestJSON(t, "decode CTP/2 here"),
		"tools": mustTestJSON(t, []any{
			map[string]any{"type": "function", "name": "null_description", "description": nil},
			map[string]any{"type": "function", "name": "object_description", "description": map[string]any{"kept": true}},
		}),
	}}

	transform, _, err := prepareCTP2TestRequest(t, codec, &request)
	if err != nil || transform == nil {
		t.Fatalf("transform = %#v, err = %v", transform, err)
	}
	tools := decodeCTP2TestValue(t, request.fields["tools"]).([]any)
	if tools[0].(map[string]any)["description"] != nil {
		t.Fatalf("null description = %#v", tools[0].(map[string]any)["description"])
	}
	object := tools[1].(map[string]any)["description"].(map[string]any)
	if object["kept"] != true {
		t.Fatalf("object description = %#v", object)
	}
}

func TestCTP2RequestUsesVisiblePriorToolOutputLines(t *testing.T) {
	codec := mustCTP2Codec(t)
	first := strings.Repeat("first exact diagnostic line with enough content to make the reference worthwhile\n", 2) +
		strings.Repeat("second exact diagnostic line with enough content to make the reference worthwhile\n", 2)
	second := "prefix\n" + first + "suffix\n"
	request := parsedResponsesRequest{fields: map[string]json.RawMessage{
		"instructions": mustTestJSON(t, "decode CTP/2 here"),
		"input": mustTestJSON(t, []any{
			map[string]any{"type": "custom_tool_call_output", "call_id": "call_α", "output": first},
			map[string]any{"type": "custom_tool_call_output", "call_id": "call_beta", "output": second},
		}),
	}}

	transform, _, err := prepareCTP2TestRequest(t, codec, &request)
	if err != nil {
		t.Fatal(err)
	}
	if transform == nil {
		t.Fatal("request was not transformed")
	}
	input := decodeCTP2TestValue(t, request.fields["input"]).([]any)
	compact := input[1].(map[string]any)["output"].(string)
	if !strings.HasPrefix(compact, ctp2VisibleLinesTag) || !strings.Contains(compact, "=") {
		t.Fatalf("visible-line output = %q", compact)
	}
	// Request references resolve against sources that precede the encoded value.
	decoded, err := decodeCTP2String(compact, transform.sources[:1], upstreamJSONBufferBytes)
	if err != nil || decoded != second {
		t.Fatalf("decoded output = %q, err = %v", decoded, err)
	}
}

func TestCTP2VisibleReferencesStayUniqueAfterRegisteringLaterSources(t *testing.T) {
	codec := mustCTP2Codec(t)
	output := "first exact diagnostic line with enough content to make the reference worthwhile\n" +
		"second exact diagnostic line with enough content to make the reference worthwhile\n"
	request := parsedResponsesRequest{fields: map[string]json.RawMessage{
		"instructions": mustTestJSON(t, "decode CTP/2 here"),
		"input": mustTestJSON(t, []any{
			map[string]any{"type": "custom_tool_call_output", "call_id": "call_abc", "output": output},
			map[string]any{"type": "custom_tool_call_output", "call_id": "call_xyzc", "output": output},
		}),
	}}

	transform, _, err := prepareCTP2TestRequest(t, codec, &request)
	if err != nil {
		t.Fatal(err)
	}
	if transform == nil {
		t.Fatal("request was not transformed")
	}
	input := decodeCTP2TestValue(t, request.fields["input"]).([]any)
	compact := input[1].(map[string]any)["output"].(string)
	decoded, err := decodeCTP2String(compact, transform.sources, upstreamJSONBufferBytes)
	if err != nil || decoded != output {
		t.Fatalf("decoded output = %q, err = %v, compact = %q", decoded, err, compact)
	}
}

func TestCTP2VisiblePrefixIsStableWhenHistoryAppends(t *testing.T) {
	codec := mustCTP2Codec(t)
	source := strings.Repeat("stable source line with enough repeated content for compression\n", 6)
	prefix := []any{
		map[string]any{"type": "custom_tool_call_output", "call_id": "call_source", "output": source},
		map[string]any{"type": "custom_tool_call_output", "call_id": "call_repeat", "output": source},
	}
	prepare := func(input []any) []json.RawMessage {
		request := parsedResponsesRequest{fields: map[string]json.RawMessage{
			"instructions": mustTestJSON(t, "decode CTP/2 here"),
			"input":        mustTestJSON(t, input),
		}}
		if transform, _, err := prepareCTP2TestRequest(t, codec, &request); err != nil || transform == nil {
			t.Fatalf("prepare transform = %#v, err = %v", transform, err)
		}
		var items []json.RawMessage
		if err := json.Unmarshal(request.fields["input"], &items); err != nil {
			t.Fatal(err)
		}
		return items
	}
	before := prepare(prefix)
	after := prepare(append(append([]any(nil), prefix...), map[string]any{
		"type": "message", "role": "assistant", "content": strings.Repeat("later text ", 20),
	}))
	if len(after) <= len(before) {
		t.Fatalf("appended input lengths = %d then %d", len(before), len(after))
	}
	for index := range before {
		if string(before[index]) != string(after[index]) {
			t.Fatalf("encoded prefix item %d changed\nbefore: %s\nafter:  %s", index, before[index], after[index])
		}
	}
}

func TestCTP2MissingInstructionCarrierStaysNative(t *testing.T) {
	codec := mustCTP2Codec(t)
	native := mustTestJSON(t, []any{map[string]any{
		"type": "message", "role": "user", "content": strings.Repeat("repeat me ", 30),
	}})
	request := parsedResponsesRequest{fields: map[string]json.RawMessage{"input": native}}
	transform, body, err := prepareCTP2TestRequest(t, codec, &request)
	if err != nil {
		t.Fatal(err)
	}
	if transform != nil || len(body) == 0 || string(request.fields["input"]) != string(native) {
		t.Fatalf("transform = %#v, body = %s, input = %s", transform, body, request.fields["input"])
	}
}

func TestCTP2DeveloperCarrierRemainsNative(t *testing.T) {
	codec := mustCTP2Codec(t)
	repeated := strings.Repeat("compress this user text; ", 20)
	request := parsedResponsesRequest{fields: map[string]json.RawMessage{
		"input": mustTestJSON(t, []any{
			map[string]any{"type": "message", "role": "developer", "content": "CTP/2 guidance"},
			map[string]any{"type": "message", "role": "user", "content": repeated},
		}),
	}}
	transform, _, err := prepareCTP2TestRequest(t, codec, &request)
	if err != nil || transform == nil {
		t.Fatalf("transform = %#v, err = %v", transform, err)
	}
	input := decodeCTP2TestValue(t, request.fields["input"]).([]any)
	if got := input[0].(map[string]any)["content"]; got != "CTP/2 guidance" {
		t.Fatalf("developer carrier = %q", got)
	}
	compact := input[1].(map[string]any)["content"].(string)
	decoded, err := decodeCTP2String(compact, transform.sources, upstreamJSONBufferBytes)
	if err != nil || decoded != repeated {
		t.Fatalf("user content decoded = %q, err = %v", decoded, err)
	}
}

func TestCTP2MultipartDeveloperCarrierEncodesSiblingText(t *testing.T) {
	codec := mustCTP2Codec(t)
	repeated := strings.Repeat("compress this developer context; ", 20)
	request := parsedResponsesRequest{fields: map[string]json.RawMessage{
		"input": mustTestJSON(t, []any{map[string]any{
			"type": "message", "role": "developer", "content": []any{
				map[string]any{"type": "input_text", "text": repeated},
				map[string]any{"type": "input_text", "text": "CTP/2 guidance"},
			},
		}}),
	}}
	transform, _, err := prepareCTP2TestRequest(t, codec, &request)
	if err != nil || transform == nil {
		t.Fatalf("transform = %#v, err = %v", transform, err)
	}
	input := decodeCTP2TestValue(t, request.fields["input"]).([]any)
	content := input[0].(map[string]any)["content"].([]any)
	compact := content[0].(map[string]any)["text"].(string)
	decoded, err := decodeCTP2String(compact, transform.sources, upstreamJSONBufferBytes)
	if err != nil || decoded != repeated {
		t.Fatalf("developer sibling decoded = %q, err = %v", decoded, err)
	}
	if got := content[1].(map[string]any)["text"]; got != "CTP/2 guidance" {
		t.Fatalf("developer carrier = %q", got)
	}
}

func TestCTP2LiteralTagsRoundTrip(t *testing.T) {
	for _, native := range []string{ctp2ReferenceTag + "@{0}", ctp2DictionaryTag + "END\n", ctp2LiteralTag + "literal", "!V+\"literal\"\n"} {
		encoded := encodeCTP2LiteralString(native)
		if !strings.HasPrefix(encoded, ctp2LiteralTag) {
			t.Fatalf("encoded literal = %q", encoded)
		}
		decoded, err := decodeCTP2String(encoded, nil, upstreamJSONBufferBytes)
		if err != nil || decoded != native {
			t.Fatalf("decoded literal = %q, err = %v", decoded, err)
		}
	}
	ctp1 := "!ctp1 R\n@{0}"
	if decoded, err := decodeCTP2String(ctp1, nil, upstreamJSONBufferBytes); err != nil || decoded != ctp1 {
		t.Fatalf("CTP/1 text under CTP/2 = %q, err = %v", decoded, err)
	}
}

func TestCTP2ResponseRestoresLocalAndVisibleAssistantText(t *testing.T) {
	codec := mustCTP2Codec(t)
	source := "first visible line\nsecond visible line\nthird visible line\n"
	transform := &ctp2ResponseTransform{sources: []ctp2VisibleLineSource{{locator: "call_alpha", lines: slicesOfLines(source)}}}
	localNative := strings.Repeat("alpha beta gamma delta epsilon; ", 12)
	local, _, err := codec.encodeContentLocalString(localNative)
	if err != nil || local == localNative {
		t.Fatalf("local = %q, err = %v", local, err)
	}
	visible := "!V=alpha,2,2\n+\"tail\"\n"
	response, err := transform.TransformJSON(mustTestJSON(t, map[string]any{
		"status": "completed",
		"output": []any{
			map[string]any{"type": "message", "role": "assistant", "content": []any{
				map[string]any{"type": "output_text", "text": local},
				map[string]any{"type": "output_text", "text": visible},
			}},
			map[string]any{"type": "custom_tool_call", "name": "exec", "input": ctp2DictionaryTag + "tool payload stays native"},
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Output []struct {
			Input   string `json:"input"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(response, &decoded); err != nil {
		t.Fatal(err)
	}
	if got := decoded.Output[0].Content[0].Text; got != localNative {
		t.Fatalf("local assistant text = %q", got)
	}
	if got := decoded.Output[0].Content[1].Text; got != "second visible line\nthird visible line\ntail" {
		t.Fatalf("visible assistant text = %q", got)
	}
	if got := decoded.Output[1].Input; got != ctp2DictionaryTag+"tool payload stays native" {
		t.Fatalf("tool payload = %q", got)
	}
}

func TestCTP2ResponseRejectsInvalidRepresentations(t *testing.T) {
	transform := &ctp2ResponseTransform{sources: []ctp2VisibleLineSource{
		{locator: "call_alpha", lines: []string{"alpha\n"}},
		{locator: "other_alpha", lines: []string{"other\n"}},
	}}
	tests := []struct {
		name string
		text string
		want string
	}{
		{name: "reference without dictionary", text: ctp2ReferenceTag + "@{0}", want: "no dictionary"},
		{name: "unknown local reference", text: ctp2DictionaryTag + "0=\"known\"\nEND\n" + ctp2ReferenceTag + "@{1}", want: "unknown reference"},
		{name: "duplicate local definition", text: ctp2DictionaryTag + "0=\"one\"\n0=\"two\"\nEND\n" + ctp2ReferenceTag + "@{0}", want: "duplicate definition"},
		{name: "empty visible payload", text: "!V", want: "empty operation"},
		{name: "blank visible operation", text: "!V\n", want: "empty operation"},
		{name: "ambiguous visible source", text: "!V=alpha,1,1\n", want: "out of range"},
		{name: "invalid visible range", text: "!V=call_alpha,2,1\n", want: "out of range"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := transform.TransformJSON(mustTestJSON(t, map[string]any{
				"output": []any{map[string]any{
					"type": "message", "role": "assistant",
					"content": []any{map[string]any{"type": "output_text", "text": test.text}},
				}},
			}))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestCTP2ExecuteRequestFallsBackToNativeOnCodecFailure(t *testing.T) {
	nativeText := strings.Repeat("alpha beta gamma delta epsilon; ", 12)
	for _, test := range []struct {
		name       string
		failCount  bool
		failEncode bool
	}{
		{name: "count", failCount: true},
		{name: "encode", failEncode: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := parsedResponsesRequest{fields: map[string]json.RawMessage{
				"model":        mustTestJSON(t, "gpt-test"),
				"instructions": mustTestJSON(t, "decode CTP/2 here"),
				"input": mustTestJSON(t, []any{
					map[string]any{"type": "message", "role": "user", "content": nativeText},
				}),
			}}
			nativeBody, err := json.Marshal(request.fields)
			if err != nil {
				t.Fatal(err)
			}
			provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(`{"status":"completed","output":[]}`)}}}
			base := mustCTP2Codec(t)
			codec := &ctp2Codec{tokens: failingCTP2Tokenizer{
				Codec:      base.tokens,
				failCount:  test.failCount,
				failEncode: test.failEncode,
			}}

			if err := executeRequest(
				t.Context(), t.Context(), request, nil, "session", provider, &bytes.Buffer{},
				nil, nil, codec, nil,
			); err != nil {
				t.Fatal(err)
			}
			if len(provider.forwarded) != 1 || !bytes.Equal(provider.forwarded[0], nativeBody) {
				t.Fatalf("forwarded request = %s, want %s", provider.forwarded[0], nativeBody)
			}
		})
	}
}

func TestCTP2StreamingRestoresEveryCompletedTextShape(t *testing.T) {
	codec := mustCTP2Codec(t)
	compactNative := strings.Repeat("streamed repeated response; ", 8)
	compact, _, err := codec.encodeContentLocalString(compactNative)
	if err != nil {
		t.Fatal(err)
	}
	transform := &ctp2ResponseTransform{}
	for _, event := range []map[string]any{
		{"type": "response.content_part.done", "part": map[string]any{"type": "output_text", "text": compact}},
		{"type": "response.output_text.done", "text": compact},
		{"type": "response.completed", "response": map[string]any{
			"output": []any{map[string]any{
				"type": "message", "role": "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": compact}},
			}},
		}},
	} {
		transformed, err := transform.TransformSSE(mustTestJSON(t, event))
		if err != nil || len(transformed) != 1 {
			t.Fatalf("TransformSSE() = %q, err = %v", transformed, err)
		}
		if strings.Contains(string(transformed[0]), ctp2DictionaryTag) {
			t.Fatalf("stream event retained CTP/2 representation: %s", transformed[0])
		}
	}
}

func TestCTP2ExecuteRequestTransformsProviderBoundary(t *testing.T) {
	codec := mustCTP2Codec(t)
	nativeText := strings.Repeat("alpha beta gamma delta epsilon; ", 12)
	compactText, _, err := codec.encodeContentLocalString(nativeText)
	if err != nil {
		t.Fatal(err)
	}
	request := parsedResponsesRequest{fields: map[string]json.RawMessage{
		"model":        mustTestJSON(t, "gpt-test"),
		"instructions": mustTestJSON(t, "decode CTP/2 here"),
		"input": mustTestJSON(t, []any{
			map[string]any{"type": "message", "role": "user", "content": nativeText},
		}),
	}}
	responseBody := string(mustTestJSON(t, map[string]any{
		"status": "completed",
		"output": []any{map[string]any{
			"type": "message", "role": "assistant",
			"content": []any{map[string]any{"type": "output_text", "text": compactText}},
		}},
	}))
	provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(responseBody)}}}
	var output bytes.Buffer
	if err := executeRequest(
		t.Context(), t.Context(), request, nil, "session", provider, &output,
		nil, nil, codec, nil,
	); err != nil {
		t.Fatal(err)
	}
	if len(provider.forwarded) != 1 || !bytes.Contains(provider.forwarded[0], []byte("!ctp2 D\\n")) {
		t.Fatalf("forwarded request = %s", provider.forwarded[0])
	}
	var visible struct {
		Output []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(output.Bytes(), &visible); err != nil {
		t.Fatal(err)
	}
	if got := visible.Output[0].Content[0].Text; got != nativeText {
		t.Fatalf("visible response text = %q", got)
	}
}

func TestCTP2DecodedSizeIsBounded(t *testing.T) {
	_, err := decodeCTP2String(
		ctp2DictionaryTag+"0=\"abcdef\"\nEND\n"+ctp2ReferenceTag+"@{0}@{0}",
		nil,
		10,
	)
	if err == nil || !strings.Contains(err.Error(), "buffer budget") {
		t.Fatalf("error = %v", err)
	}
}

func mustCTP2Codec(t testing.TB) *ctp2Codec {
	t.Helper()
	codec, err := newCTP2Codec()
	if err != nil {
		t.Fatal(err)
	}
	return codec
}

func decodeCTP2TestValue(t *testing.T, raw json.RawMessage) any {
	t.Helper()
	var value any
	err := json.Unmarshal(raw, &value)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func slicesOfLines(value string) []string {
	return slices.Collect(strings.Lines(value))
}

type failingCTP2Tokenizer struct {
	tokenizer.Codec
	failCount  bool
	failEncode bool
}

func (c failingCTP2Tokenizer) Count(value string) (int, error) {
	if c.failCount {
		return 0, errors.New("injected count failure")
	}
	return c.Codec.Count(value)
}

func (c failingCTP2Tokenizer) Encode(value string) ([]uint, []string, error) {
	if c.failEncode {
		return nil, nil, errors.New("injected encode failure")
	}
	return c.Codec.Encode(value)
}

func prepareCTP2TestRequest(t testing.TB, codec *ctp2Codec, request *parsedResponsesRequest) (*ctp2ResponseTransform, []byte, error) {
	t.Helper()
	native, err := request.wireBody(request.fields)
	if err != nil {
		t.Fatal(err)
	}
	return codec.prepareRequest(request, native)
}

func TestDecodeJSONStringMatchesJSON(t *testing.T) {
	for _, raw := range []string{
		`""`, `"text"`, `"escaped\n\u1234"`, `null`, " \r\n\t\"text\"\t ",
		`[]`, `[{"content":"text"}]`, `{}`, `123`, `true`, `false`,
		``, ` `, `"unterminated`, `null false`, `"text" true`, "\x00\"text\"",
		"\u00a0\"text\"", "\"\xff\"", `"a\uD800"`,
	} {
		t.Run(raw, func(t *testing.T) {
			var want string
			err := json.Unmarshal([]byte(raw), &want)
			got, ok := decodeJSONString([]byte(raw))
			if ok != (err == nil) || (ok && got != want) {
				t.Fatalf("decode = %q, %v; JSON = %q, %v", got, ok, want, err)
			}
		})
	}
}

func BenchmarkDecodeJSONStringMultipart(b *testing.B) {
	raw := mustMarshalJSON([]any{map[string]string{
		"type": "input_text", "text": strings.Repeat("provider-owned content\n", 1000),
	}})
	b.Run("unmarshal", func(b *testing.B) {
		for b.Loop() {
			var text string
			_ = json.Unmarshal(raw, &text)
		}
	})
	b.Run("dispatch", func(b *testing.B) {
		for b.Loop() {
			_, _ = decodeJSONString(raw)
		}
	})
}
