//go:build codex_e2e

package launch

import (
	"context"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"

	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yusing/mekugi/backends/codex/appserver"
)

// This checks the UserInput RPC boundary, not native sibling delivery.
// Codex 0.162.1 loses UserInput origin before provider delivery. A changed wire
// shape requires reassessing this limitation before excluding sibling input
// from session naming. Explicit client IDs probe the strongest
// existing RPC support; ordinary orchestration delivery does not submit them.
func TestAppServerUserInputProvenanceNativeCodexE2E(t *testing.T) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	version, err := exec.Command(codex, "--version").Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Log(strings.TrimSpace(string(version)))
	const routed = "Message Type: MESSAGE\nTask name: /root/recipient\nSender: /root/sibling\nPayload:\nWhich acceptance check failed?"
	const human = "Keep the original user request for session naming."
	for _, builtIn := range []bool{false, true} {
		name := "custom"
		if builtIn {
			name = "openai"
		}
		t.Run(name, func(t *testing.T) {
			var siblingItem map[string]any
			for _, trial := range []string{"routed UserInput", "human quotation", "mixed steers", "mixed queued fragments", "mixed joined draft"} {
				t.Run(trial, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
					defer cancel()
					requests := make(chan []byte, 8)
					gate := make(chan struct{})
					defer close(gate)
					var count atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
							http.Error(w, "fixture requires HTTP", http.StatusUpgradeRequired)
							return
						}
						body, err := io.ReadAll(r.Body)
						if err != nil {
							http.Error(w, err.Error(), http.StatusBadRequest)
							return
						}
						select {
						case requests <- body:
						case <-ctx.Done():
							return
						}
						if strings.HasPrefix(trial, "mixed") && trial != "mixed joined draft" && count.Add(1) == 1 {
							select {
							case <-gate:
							case <-ctx.Done():
								return
							}
						}
						response := routerFaultCodexSuccessResponse()
						defer response.Body.Close()
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.Copy(w, response.Body)
					}))
					defer func() { cancel(); server.Close() }()
					provider := `model_providers.provenance={name="provenance",base_url=` + strconv.Quote(server.URL+"/v1") + `,wire_api="responses",requires_openai_auth=false}`
					selection := `model_provider="provenance"`
					var environment []string
					if builtIn {
						provider = "openai_base_url=" + strconv.Quote(server.URL+"/v1")
						selection = `model_provider="openai"`
						environment = builtInOpenAICodexEnvironment(t)
					} else {
						environment = routerFaultCodexEnvironment(t)
					}
					cmd := exec.CommandContext(ctx, codex, "app-server", "-c", provider, "-c", selection, "-c", `model="gpt-6-astra"`, "-c", "features.plugins=false", "-c", "features.goals=false", "-c", "include_collaboration_mode_instructions=false")
					cmd.Env, cmd.Dir = environment, t.TempDir()
					client, err := appserver.Start(cmd)
					if err != nil {
						t.Fatal(err)
					}
					defer client.Close()
					var notifications []appserver.Message
					pump := func(done func(appserver.Message) bool) appserver.Message {
						t.Helper()
						for {
							select {
							case m, ok := <-client.Messages:
								if !ok {
									t.Fatal("isolated host closed")
								}
								if m.Method != "" {
									notifications = append(notifications, m)
								}
								if done(m) {
									return m
								}
							case <-ctx.Done():
								t.Fatalf("host timeout: %v; notifications=%s", ctx.Err(), responses.MustMarshalJSON(notifications))
							}
						}
					}
					rpc := func(method string, params any) appserver.Message {
						t.Helper()
						id, err := client.Send(method, params, true)
						if err != nil {
							t.Fatal(err)
						}
						m := pump(func(m appserver.Message) bool { return string(m.ID) == id })
						if m.Error != nil {
							t.Fatalf("%s: %+v", method, m.Error)
						}
						return m
					}
					id, err := client.Initialize()
					if err != nil {
						t.Fatal(err)
					}
					initialized := pump(func(m appserver.Message) bool { return string(m.ID) == id })
					if initialized.Error != nil {
						t.Fatalf("initialize: %+v", initialized.Error)
					}
					if _, err := client.Send("initialized", map[string]any{}, false); err != nil {
						t.Fatal(err)
					}
					start := rpc("thread/start", map[string]any{"cwd": cmd.Dir, "approvalPolicy": "never"})
					var thread struct {
						Thread struct {
							ID string `json:"id"`
						} `json:"thread"`
					}
					if err := json.Unmarshal(start.Result, &thread); err != nil || thread.Thread.ID == "" {
						t.Fatalf("invalid thread: %s", start.Result)
					}
					text := routed
					if strings.HasPrefix(trial, "mixed") {
						text = "Initial human question."
					}
					if trial == "mixed joined draft" {
						text = routed + "\n" + human // Existing idle delivery joins queued input before appserver.Input.
					}
					clientID := "sibling-id"
					if trial != "routed UserInput" {
						clientID = "human-id"
					}
					turnResult := rpc("turn/start", map[string]any{"threadId": thread.Thread.ID, "input": appserver.Input(text), "clientUserMessageId": clientID})
					var turn struct {
						Turn struct {
							ID string `json:"id"`
						} `json:"turn"`
					}
					if err := json.Unmarshal(turnResult.Result, &turn); err != nil || turn.Turn.ID == "" {
						t.Fatalf("invalid turn: %s", turnResult.Result)
					}
					nextRequest := func() []byte {
						t.Helper()
						select {
						case body := <-requests:
							return body
						case <-ctx.Done():
							t.Fatal("no provider request")
							return nil
						}
					}
					body := nextRequest()
					wantedTexts := [][]string{{text}}
					if trial == "mixed steers" {
						for i, text := range []string{routed, human} {
							rpc("turn/steer", map[string]any{"threadId": thread.Thread.ID, "expectedTurnId": turn.Turn.ID, "input": appserver.Input(text), "clientUserMessageId": []string{"sibling-id", "human-id"}[i]})
						}
						gate <- struct{}{}
						body = nextRequest()
						wantedTexts = [][]string{{routed}, {human}}
					} else if trial == "mixed queued fragments" {
						input := append(appserver.Input(routed), appserver.Input(human)...)
						rpc("turn/steer", map[string]any{"threadId": thread.Thread.ID, "expectedTurnId": turn.Turn.ID, "input": input, "clientUserMessageId": "mixed-id"})
						gate <- struct{}{}
						body = nextRequest()
						wantedTexts = [][]string{{routed, human}}
					}
					completed := pump(func(m appserver.Message) bool { return m.Method == "turn/completed" })
					var terminal struct {
						Turn struct {
							ID     string `json:"id"`
							Status string `json:"status"`
						} `json:"turn"`
					}
					if err := json.Unmarshal(completed.Params, &terminal); err != nil || terminal.Turn.ID != turn.Turn.ID || terminal.Turn.Status != "completed" {
						t.Fatalf("host turn did not complete successfully: %s", completed.Params)
					}
					var request struct {
						Input []map[string]any `json:"input"`
					}
					if err := json.Unmarshal(body, &request); err != nil {
						t.Fatal(err)
					}
					var userItems []map[string]any
					for _, item := range request.Input {
						if item["role"] == "user" {
							userItems = append(userItems, item)
						}
					}
					var hostItems []map[string]any
					for _, m := range notifications {
						if m.Method != "item/completed" {
							continue
						}
						var params struct {
							Item map[string]any `json:"item"`
						}
						if err := json.Unmarshal(m.Params, &params); err != nil {
							t.Fatal(err)
						}
						if params.Item["type"] == "userMessage" {
							t.Logf("exact host user notification: %s", m.Params)
							hostItems = append(hostItems, params.Item)
						}
					}
					if len(userItems) < len(wantedTexts) || len(hostItems) < len(wantedTexts) {
						t.Fatalf("missing provider/host user items: %d/%d", len(userItems), len(hostItems))
					}
					userItems = userItems[len(userItems)-len(wantedTexts):]
					hostItems = hostItems[len(hostItems)-len(wantedTexts):]
					for i, texts := range wantedTexts {
						item, host := userItems[i], hostItems[i]
						t.Logf("exact provider user item: %s", responses.MustMarshalJSON(item))
						providerID, _ := item["id"].(string)
						hostID, _ := host["id"].(string)
						if providerID == "" || hostID == "" || providerID == hostID || providerID == "msg_"+hostID {
							t.Fatal("host/provider ID relation changed; reassess authenticated item joins")
						}
						delete(item, "id")
						var content, hostContent, kinds []any
						for _, text := range texts {
							content = append(content, map[string]any{"type": "input_text", "text": text})
							hostContent = append(hostContent, map[string]any{"type": "text", "text": text, "text_elements": []any{}})
							kinds = append(kinds, "user.text")
						}
						if builtIn {
							metadata, ok := item["internal_chat_message_metadata_passthrough"].(map[string]any)
							if !ok || metadata["turn_id"] != turn.Turn.ID || metadata["create_time"] == nil {
								t.Fatalf("host classification shape changed: %v", metadata)
							}
							delete(metadata, "turn_id")
							delete(metadata, "create_time")
							if !reflect.DeepEqual(metadata, map[string]any{"content_item_kinds": kinds}) {
								t.Fatalf("host origin classification changed: %v", metadata)
							}
							delete(item, "internal_chat_message_metadata_passthrough")
						}
						if !reflect.DeepEqual(item, map[string]any{"type": "message", "role": "user", "content": content}) {
							t.Fatalf("provider origin shape changed: %s", responses.MustMarshalJSON(item))
						}
						wantClientID := clientID
						if trial == "mixed steers" {
							wantClientID = []string{"sibling-id", "human-id"}[i]
						}
						if trial == "mixed queued fragments" {
							wantClientID = "mixed-id"
						}
						if !reflect.DeepEqual(host, map[string]any{"type": "userMessage", "id": hostID, "clientId": wantClientID, "content": hostContent}) {
							t.Fatalf("host origin/fragment shape changed: %s", responses.MustMarshalJSON(host))
						}
						if trial == "routed UserInput" {
							siblingItem = item
						}
						if trial == "human quotation" && !reflect.DeepEqual(item, siblingItem) {
							t.Fatal("human quotation and routed UserInput differ beyond unrelated host IDs/timestamps")
						}
					}
				})
			}
		})
	}
}
