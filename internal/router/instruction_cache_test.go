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
	for _, compact := range []bool{false, true} {
		t.Run(map[bool]string{false: "native", true: "ctp"}[compact], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			proxy := newToolPluginTestProxy(t)
			proxy.customizedInstructions = true
			var codec *ctp2Codec
			if compact {
				codec = mustCTP2Codec(t)
			}
			proxy.compactModelProtocol = compact
			base := []any{testCodeModeAdditionalTools(testCodeModeDescription), map[string]string{"type": "message", "role": "developer", "content": "Follow the task."}}
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
				for index, id := range ids {
					request, err := providerSocketRead(ctx, upstream)
					if err != nil {
						t.Error(err)
						return
					}
					var input []json.RawMessage
					_ = json.Unmarshal(request["input"], &input)
					switch id {
					case "warm":
						if !sameJSONValue(request["input"], mustMarshalJSON(base)) || string(request["generate"]) != "false" {
							t.Error("prewarm changed native instructions or tool descriptions")
						}
					case "turn", "astra":
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
			}), proxy, codec, headers)
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
					request["input"] = base
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
		})
	}
}

func TestInstructionCacheAutomaticSuccessorCannotSilentlyDropChanges(t *testing.T) {
	exchange := &webSocketExchange{automatic: true, history: &webSocketHistory{}}
	request := &parsedResponsesRequest{cachedInput: 1}
	err := exchange.prepareInstructionCache(request, []byte(`{"input":[{"role":"developer","content":"changed"}]}`))
	if err == nil || !strings.Contains(err.Error(), "cached instructions") {
		t.Fatalf("automatic successor silently accepted a different prefix: %v", err)
	}
}
