package inferencegateway

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/providers/grok"
)

func TestSubagentBridgeConfiguredProviderGuidance(t *testing.T) {
	for _, tc := range []struct {
		name     string
		grok     bool
		openCode []string
	}{
		{name: "ordinary"},
		{name: "grok", grok: true},
		{name: "opencode", openCode: []string{"opencode-go:glm-5.3-flash", "opencode-zen:grok-4.6"}},
		{name: "combined", grok: true, openCode: []string{"opencode-go:glm-5.3-flash"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, err := codexresponses.ParseRequest([]byte(`{"tools":[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent","description":"Native lifecycle rules","parameters":{"type":"object","properties":{"model":{"type":"string"},"fork_turns":{"type":"string"},"reasoning_effort":{"type":"string"}}}}]}]}`))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := codexresponses.PrepareCollaboration(&request, collaborationGuidance(tc.grok, false, tc.openCode...)); err != nil {
				t.Fatal(err)
			}
			var tools []struct {
				Tools []struct {
					Description string
					Parameters  struct {
						Properties map[string]struct{ Description string }
					}
				}
			}
			if err := json.Unmarshal(request.Fields["tools"], &tools); err != nil {
				t.Fatal(err)
			}
			spawn := tools[0].Tools[0]
			if tc.grok {
				for _, model := range grok.Models() {
					if strings.Count(spawn.Parameters.Properties["model"].Description, "`grok:"+model+"`") != 1 {
						t.Fatalf("missing or duplicated Grok model %q", model)
					}
				}
				for name, want := range map[string]string{"fork_turns": grokForkTurnsInstruction, "reasoning_effort": grokReasoningEffortInstruction} {
					if got := spawn.Parameters.Properties[name].Description; got != want {
						t.Fatalf("%s guidance = %q, want %q", name, got, want)
					}
				}
			} else {
				for name, property := range spawn.Parameters.Properties {
					if property.Description != "" {
						t.Fatalf("unexpected %s guidance: %q", name, property.Description)
					}
				}
			}
			if !strings.HasPrefix(spawn.Description, "Native lifecycle rules") {
				t.Fatalf("native spawn guidance lost: %q", spawn.Description)
			}
			if len(tc.openCode) == 0 {
				if spawn.Description != "Native lifecycle rules" {
					t.Fatalf("unexpected spawn guidance: %q", spawn.Description)
				}
			} else {
				for _, model := range tc.openCode {
					if strings.Count(spawn.Description, model) != 1 {
						t.Fatalf("missing or duplicated OpenCode model %q: %q", model, spawn.Description)
					}
				}
				prefix, advice, found := strings.Cut(opencodeSpawnInstruction, "%MODELS%")
				if !found || !strings.Contains(spawn.Description, prefix) || !strings.HasSuffix(spawn.Description, advice) {
					t.Fatalf("OpenCode provider advice changed: %q", spawn.Description)
				}
			}
			if bytes.Contains(request.Fields["tools"], []byte("%MODELS%")) {
				t.Fatal("unexpanded provider model catalog")
			}
		})
	}
}

func TestGrokDedicatedSpawnGuidance(t *testing.T) {
	request := bridgeTestRequest(t, false)
	if _, err := codexresponses.PrepareCollaboration(&request, collaborationGuidance(true, true)); err != nil {
		t.Fatal(err)
	}
	for _, model := range grok.Models() {
		if !bytes.Contains(request.Fields["tools"], []byte("`"+model+"`")) {
			t.Fatalf("dedicated Grok model missing from spawn guidance: %s", model)
		}
	}
	if bytes.Contains(request.Fields["tools"], []byte("grok:")) {
		t.Fatal("dedicated Grok spawn guidance advertises namespaced models")
	}
}

func bridgeTestRequest(t *testing.T, additional bool) codexresponses.Request {
	t.Helper()
	ns := map[string]any{"type": "namespace", "name": "collaboration", "tools": []any{map[string]any{"type": "function", "name": "spawn_agent", "parameters": map[string]any{"type": "object", "properties": map[string]any{"message": map[string]any{"type": "string", "encrypted": true}, "model": map[string]string{"type": "string"}}}}}}
	request := map[string]any{"model": "gpt-test", "instructions": "keep", "tools": []any{ns}, "input": []any{map[string]string{"type": "function_call", "namespace": "collaboration", "name": "spawn_agent", "arguments": "{}", "call_id": "c1"}}}
	if additional {
		request["tools"] = []any{}
		request["input"] = append(request["input"].([]any), map[string]any{"type": "additional_tools", "tools": []any{ns}})
	}
	parsed, err := codexresponses.ParseRequest(mustTestJSON(t, request))
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
