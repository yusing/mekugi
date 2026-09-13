package router

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/yusing/mekugi/capturer"
	codexinstructions "github.com/yusing/mekugi/contrib/codex"
)

func TestCaptureShellMisuseAndInstructionRewrite(t *testing.T) {
	t.Parallel()
	for _, recovery := range []bool{false, true} {
		for _, native := range []bool{false, true} {
			for _, streaming := range []bool{false, true} {
				t.Run("recovery="+strconv.FormatBool(recovery)+"/native="+strconv.FormatBool(native)+"/stream="+strconv.FormatBool(streaming), func(t *testing.T) {
					capturePath := filepath.Join(t.TempDir(), "capture.jsonl")
					flags := newRouterFlags(io.Discard)
					*flags.debug = true
					debug, err := openDebugOutput(flags)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = debug.close(); _ = os.RemoveAll(filepath.Dir(debug.paths[0])) })
					recorder, err := capturer.New(capturer.Config{Mode: "mekugi", ModelProtocol: "native", Output: capturePath})
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = recorder.Close() })
					privateScript := `console.log("private-script-sentinel")`
					diagnostic := "shell-typescript-misuse"
					if recovery {
						privateScript = `const r = await tools.exec_command({cmd: "echo private-script-sentinel"}); text(r);`
						if !native {
							diagnostic = "shell-code-mode-recovered"
						}
					}
					item := map[string]any{"type": "custom_tool_call", "name": "shell", "id": "item-shell", "call_id": "call-shell", "input": privateScript, "status": "completed"}
					terminal := map[string]any{"status": "completed", "output": []any{item}}
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						body, _ := io.ReadAll(r.Body)
						var request map[string]json.RawMessage
						if err := json.Unmarshal(body, &request); err != nil {
							t.Error(err)
						}
						instructions := jsonString(request, "instructions")
						dump, err := os.ReadFile(debug.paths[3])
						var exported map[string]json.RawMessage
						if err != nil || json.Unmarshal(dump, &exported) != nil {
							t.Errorf("read instruction dump: %v", err)
						}
						if !sameJSONValue(exported["instructions"], request["instructions"]) || !sameJSONValue(exported["tools"], request["tools"]) {
							t.Error("dump does not match final upstream instructions and tools")
						}
						if bytes.Contains(dump, []byte("private-script-sentinel")) {
							t.Error("dump included a tool call")
						}
						if !strings.Contains(instructions, codexinstructions.InstructionsForModel("gpt-5.6-luna", false)) || strings.Contains(instructions, stockExecInstruction) {
							t.Error("Astra-shaped override was not rewritten for Luna")
						}
						if streaming {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = w.Write(append(append([]byte("data: "), mustTestJSON(t, map[string]any{"type": "response.output_item.done", "item": item})...), '\n', '\n'))
							_, _ = w.Write(append(append([]byte("data: "), mustTestJSON(t, map[string]any{"type": "response.completed", "response": terminal})...), '\n', '\n'))
						} else {
							w.Header().Set("Content-Type", "application/json")
							_, _ = w.Write(mustTestJSON(t, terminal))
						}
					}))
					t.Cleanup(upstream.Close)
					provider := captureReplayProvider{client: &http.Client{Transport: recorder.Transport(http.DefaultTransport)}, url: upstream.URL}
					proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
					proxy.customizedInstructions = true
					headers := serverMetadataHeaders(t, "turn", map[string]json.RawMessage{t.TempDir(): nil})
					handler := recorder.Handler(debug.handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						body, _ := io.ReadAll(r.Body)
						parsed, err := parseResponsesRequest(body)
						if err != nil {
							t.Error(err)
							return
						}
						if err := executeRequest(r.Context(), r.Context(), parsed, headers, "capture-diagnostic", provider, w, nil, proxy, nil, nil); err != nil {
							t.Error(err)
						}
					})))
					initial := serverRequest(t, func(fields map[string]any) {
						fields["model"] = "gpt-5.6-luna"
						fields["instructions"] = stockAstraIntroduction + "\n\n" + stockWorkHeading + "\n\n" + stockRGInstruction + "\n" + stockExecInstruction + "\nprivate-prompt-sentinel"
						if native {
							fields["tools"] = testNativeResponsesTools()
							fields["input"] = []any{}
						}
					})
					request := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(initial.originalBody))
					request.Header = headers.Clone()
					response := httptest.NewRecorder()
					handler.ServeHTTP(response, request)
					if !strings.Contains(response.Body.String(), diagnostic) {
						t.Fatalf("missing diagnostic: %s", response.Body.String())
					}
					var metrics bytes.Buffer
					if err := recorder.WriteMetrics(&metrics); err != nil {
						t.Fatal(err)
					}
					var snapshot struct {
						Exchanges []struct {
							InstructionRewrite capturer.InstructionRewrite `json:"instruction_rewrite"`
							DeliveredTools     []struct {
								CallID     string `json:"call_id"`
								Diagnostic string `json:"diagnostic"`
							} `json:"delivered_tools"`
						} `json:"exchanges"`
					}
					if err := json.Unmarshal(metrics.Bytes(), &snapshot); err != nil {
						t.Fatal(err)
					}
					want := capturer.InstructionRewrite{Carrier: "instructions", Strategy: "stock-astra", Workflow: "default", CustomConfigured: true}
					if len(snapshot.Exchanges) != 1 || snapshot.Exchanges[0].InstructionRewrite != want {
						t.Fatalf("rewrite evidence: %s", metrics.String())
					}
					tools := snapshot.Exchanges[0].DeliveredTools
					if len(tools) != 1 || tools[0].CallID != "call-shell" || tools[0].Diagnostic != diagnostic {
						t.Fatalf("misuse evidence: %+v", tools)
					}
					data, err := os.ReadFile(capturePath)
					if err != nil {
						t.Fatal(err)
					}
					compactMetrics := new(bytes.Buffer)
					if err := json.Compact(compactMetrics, metrics.Bytes()); err != nil {
						t.Fatal(err)
					}
					for _, output := range [][]byte{data, compactMetrics.Bytes()} {
						if bytes.Contains(output, []byte("private-script-sentinel")) || bytes.Contains(output, []byte("private-prompt-sentinel")) {
							t.Fatal("capture retained private content")
						}
						if !bytes.Contains(output, []byte(`"strategy":"stock-astra"`)) || !bytes.Contains(output, []byte(`"diagnostic":"`+diagnostic+`"`)) {
							t.Fatal("missing durable diagnosis")
						}
					}
				})
			}
		}
	}
}

func TestCaptureInstructionCarrierAndFailures(t *testing.T) {
	for _, test := range []struct {
		name, carrier, strategy string
		fields                  map[string]any
	}{
		{"missing", "none", "unchanged", map[string]any{}},
		{"null", "none", "unchanged", map[string]any{"instructions": nil}},
		{"custom", "instructions", "custom-append", map[string]any{"instructions": "private custom prompt"}},
		{"invalid type", "instructions", "rejected", map[string]any{"instructions": 42}},
		{"invalid markers", "instructions", "rejected", map[string]any{"instructions": mekugiInstructionsStartMarker}},
		{"developer fallback", "developer", "stock-gpt5", map[string]any{"instructions": "", "input": []any{map[string]any{"type": "message", "role": "developer", "content": stockModelInstructionsForTest("", "")}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder, err := capturer.New(capturer.Config{Mode: "mekugi", ModelProtocol: "native"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = recorder.Close() })
			handler := recorder.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				request, err := parseResponsesRequest(body)
				if err != nil {
					t.Error(err)
					return
				}
				err = rewriteReceivedModelInstructions(r.Context(), &request, true, codexinstructions.InstructionsForModel("", false))
				if (err != nil) != (test.strategy == "rejected") {
					t.Errorf("rewrite error: %v", err)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":{"message":"fixture"}}`)
			}))
			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(mustTestJSON(t, test.fields))))
			var output bytes.Buffer
			if err := recorder.WriteMetrics(&output); err != nil {
				t.Fatal(err)
			}
			var snapshot struct {
				Exchanges []struct {
					Rewrite capturer.InstructionRewrite `json:"instruction_rewrite"`
				} `json:"exchanges"`
			}
			if err := json.Unmarshal(output.Bytes(), &snapshot); err != nil {
				t.Fatal(err)
			}
			want := capturer.InstructionRewrite{Carrier: test.carrier, Strategy: test.strategy, Workflow: "default", CustomConfigured: true}
			if len(snapshot.Exchanges) != 1 || snapshot.Exchanges[0].Rewrite != want {
				t.Fatalf("unexpected evidence: %s", output.String())
			}
		})
	}
}
