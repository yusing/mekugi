package router

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// Exercise the provider wire, not the locally reconstructed instruction dump.
func TestWebSocketPrewarmInstructionDelivery(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	proxy := newToolPluginTestProxy(t)
	proxy.customizedInstructions = true
	base := []any{testCodeModeAdditionalTools(testCodeModeDescription), map[string]string{"type": "message", "role": "developer", "content": "Follow the task."}}
	incoming := []any{base[0], map[string]string{"type": "message", "role": "developer", "content": "Follow the task." + instructionOmitStart + "omitted-rtk-policy" + instructionOmitEnd}}
	ids := []string{"warm", "turn", "next", "astra", "astra-next"}
	headers := codexAuthHeaders()
	headers.Set(sessionIDHeader, "instruction-cache-session")
	directory := t.TempDir()
	conn := testResponsesSocket(t, ctx, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		upstream.SetReadLimit(upstreamJSONBufferBytes)
		defer upstream.CloseNow()
		var warmedTools json.RawMessage
		for index, id := range ids {
			request, err := providerSocketRead(ctx, upstream)
			if err != nil {
				t.Error(err)
				return
			}
			if bytes.Contains(request["input"], []byte("omitted-rtk-policy")) || bytes.Contains(request["input"], []byte("mekugi:omit")) {
				t.Error("provider received omitted instructions")
			}
			var input []json.RawMessage
			_ = json.Unmarshal(request["input"], &input)
			switch id {
			case "warm":
				warmedTools = bytes.Clone(request["tools"])
				if string(request["generate"]) != "false" || !bytes.Contains(request["input"], []byte("mekugi-model-instructions:start")) ||
					bytes.Contains(request["input"], []byte("tools.exec_command")) ||
					!bytes.Contains(request["tools"], []byte(`"shell"`)) || !bytes.Contains(request["tools"], []byte(`"journal"`)) {
					t.Error("prewarm did not project non-generating turn instructions and tools")
				}
			case "turn":
				if !sameJSONValue(warmedTools, request["tools"]) {
					t.Error("first turn changed the warmed tool catalog")
				}
				if jsonString(request, "previous_response_id") != "warm" || len(input) != 1 {
					t.Errorf("first turn discarded warmed prefix: parent=%q items=%d", jsonString(request, "previous_response_id"), len(input))
				}
			case "astra":
				if jsonString(request, "previous_response_id") != "" || len(input) != index+2 {
					t.Errorf("%s did not replace the stale provider prefix: parent=%q items=%d", id, jsonString(request, "previous_response_id"), len(input))
				}
				if !bytes.Contains(request["input"], []byte("mekugi-model-instructions:start")) || bytes.Contains(request["input"], []byte("tools.exec_command")) {
					t.Errorf("%s did not deliver patched guidance and catalog", id)
				}
			case "next", "astra-next":
				if jsonString(request, "previous_response_id") != ids[index-1] || len(input) != 1 {
					t.Errorf("%s unnecessarily resent unchanged history", id)
				}
			}
			if err := providerSocketWrite(ctx, upstream, socketEvent("response.completed", id)); err != nil {
				t.Error(err)
				return
			}
		}
		_, _, _ = upstream.Read(ctx)
	}), proxy, headers)
	for index, id := range ids {
		model := "gpt-5.6-luna"
		if index >= 3 {
			model = "gpt-6-astra"
		}
		request := map[string]any{"type": "response.create", "model": model}
		metadata := codexTurnMetadata{RequestKind: "turn", Directories: map[string]json.RawMessage{directory: nil}}
		if index == 0 {
			metadata.RequestKind = "prewarm"
			request["generate"] = false
			request["input"] = incoming
		} else {
			request["previous_response_id"] = ids[index-1]
			request["input"] = []any{map[string]string{"role": "user", "content": id}}
		}
		request["client_metadata"] = map[string]string{codexTurnMetadataHeader: string(mustMarshalJSON(metadata)), threadIDHeader: "instruction-cache-thread"}
		socketWrite(t, ctx, conn, request)
		if event := socketRead(t, ctx, conn); jsonString(event, "type") != "response.completed" {
			t.Fatalf("%s failed: %s", id, mustMarshalJSON(event))
		}
	}
}

func TestInstructionCacheAutomaticSuccessorCannotSilentlyDropChanges(t *testing.T) {
	exchange := &webSocketExchange{automatic: true, history: &webSocketHistory{}}
	request := &parsedResponsesRequest{cachedInput: 1}
	err := exchange.reconcileProviderHistory(request, []byte(`{"input":[{"role":"developer","content":"changed"}]}`))
	if err == nil || !strings.Contains(err.Error(), "cached provider history") {
		t.Fatalf("automatic successor silently accepted a different prefix: %v", err)
	}
}

func TestPrewarmUnsupportedCatalogRemainsNative(t *testing.T) {
	proxy := newToolPluginTestProxy(t)
	request, err := parseResponsesRequest([]byte(`{"model":"gpt-test","generate":false,"instructions":"native instructions","tools":[{"type":"web_search"}],"input":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	before := mustMarshalJSON(request.fields)
	transform, err := proxy.prepareModelRequest(t.Context(), &request, "", "", codexTurnMetadata{RequestKind: "prewarm"}, true, true)
	if err != nil || transform != nil {
		t.Fatalf("native handshake initialized execution or failed: %v, %v", transform, err)
	}
	if !sameJSONValue(before, mustMarshalJSON(request.fields)) {
		t.Fatal("unsupported prewarm catalog was partially rewritten")
	}
}

func TestPrewarmMalformedCatalogRejectsWithoutPanic(t *testing.T) {
	proxy := newToolPluginTestProxy(t)
	for _, catalog := range []string{`[42]`, `"invalid"`, `[{"type":"function","name":"lookup"},42]`} {
		request, err := parseResponsesRequest([]byte(`{"model":"gpt-test","generate":false,"tools":` + catalog + `,"input":[]}`))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := proxy.prepareModelRequest(t.Context(), &request, "", "", codexTurnMetadata{RequestKind: "prewarm"}, true, true); err == nil {
			t.Fatalf("malformed catalog accepted: %s", catalog)
		}
	}
}
