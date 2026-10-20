// Source: apps/mekugi/internal/router/{grok_launch_test.go,subagent_bridge_test.go,opencode_test.go}@[8877886e] catalog cases.
package modelcatalog

import (
	"bytes"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"testing"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/providers/grok"
	"github.com/yusing/mekugi/providers/opencode"
)

func TestGrokLaunchCatalogNamespaces(t *testing.T) {
	for _, unprefixed := range []bool{false, true} {
		t.Run(fmt.Sprint(unprefixed), func(t *testing.T) {
			session := Selection{GrokEnabled: true, GrokUnprefixed: unprefixed}
			body, err := Build([]byte(`{"models":[{"slug":"gpt-6-sol","multi_agent_version":"v2","shell_type":"unified_exec","apply_patch_tool_type":"freeform"}]}`), session)
			if err != nil {
				t.Fatal(err)
			}
			var catalog struct {
				Models []struct {
					Slug  string `json:"slug"`
					Shell string `json:"shell_type"`
				} `json:"models"`
			}
			if err := json.Unmarshal(body, &catalog); err != nil || len(catalog.Models) != len(grok.Models()) {
				t.Fatalf("third-party-only catalog: %s, %v", body, err)
			}
			for i, entry := range catalog.Models {
				want := grok.Models()[i]
				if !unprefixed {
					want = "grok:" + want
				}
				if entry.Slug != want || entry.Shell != "unified_exec" {
					t.Fatalf("catalog model = %+v, want %s", entry, want)
				}
			}
			repeated, err := Build(body, session)
			if err != nil || !bytes.Equal(body, repeated) {
				t.Fatalf("session catalog is not idempotent: %v", err)
			}
		})
	}
}

func TestGrokCatalogPreservesNativeMetadata(t *testing.T) {
	catalog := []byte(`{"models":[{"slug":"gpt-5.6-sol","multi_agent_version":"v2","use_responses_lite":true,"model_messages":{"instructions_template":"native instructions"},"unknown_future_field":42}],"extra":"keep"}`)
	result, err := Build(catalog, Selection{GrokEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Models []map[string]jsonv1.RawMessage `json:"models"`
		Extra  string                         `json:"extra"`
	}
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Models) != len(grok.Models()) || parsed.Extra != "keep" || responses.StringField(parsed.Models[1], "slug") != "grok:grok-4.6" || string(parsed.Models[1]["unknown_future_field"]) != "42" {
		t.Fatalf("catalog=%s", result)
	}
	for index, id := range grok.Models() {
		if got := responses.StringField(parsed.Models[index], "slug"); got != "grok:"+id {
			t.Fatalf("catalog model %d = %q", index, got)
		}
		if string(parsed.Models[index]["model_messages"]) != `{"instructions_template":"native instructions"}` {
			t.Fatal("native instructions were not preserved")
		}
	}
	if responses.StringField(parsed.Models[1], "visibility") != "list" || string(parsed.Models[1]["supported_in_api"]) != "true" {
		t.Fatal("Grok is not available in the main-agent model picker")
	}
	if string(parsed.Models[1]["use_responses_lite"]) != "false" {
		t.Fatal("inherited OpenAI lite transport")
	}
	repeated, err := Build(result, Selection{GrokEnabled: true})
	if err != nil || !bytes.Equal(result, repeated) {
		t.Fatalf("catalog changed when pinning a cached Grok entry: %v", err)
	}
}

func TestGrokCatalogRequiresAnActualV2Template(t *testing.T) {
	for _, tc := range []struct {
		name, models, marker string
	}{
		{"missing", `[{"slug":"gpt-6-sol","multi_agent_version":"v1"}]`, ""},
		{"fallback", `[{"slug":"gpt-6-sol","multi_agent_version":"v1"},{"slug":"other","multi_agent_version":"v2","marker":"fallback"}]`, "fallback"},
		{"preferred", `[{"slug":"other","multi_agent_version":"v2","marker":"fallback"},{"slug":"gpt-6-sol","multi_agent_version":"v2","marker":"preferred"}]`, "preferred"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := Build([]byte(`{"models":`+tc.models+`}`), Selection{GrokEnabled: true})
			if tc.marker == "" {
				if err == nil {
					t.Fatal("accepted non-v2 catalog")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var catalog struct {
				Models []map[string]jsonv1.RawMessage `json:"models"`
			}
			if err := json.Unmarshal(result, &catalog); err != nil {
				t.Fatal(err)
			}
			if len(catalog.Models) != len(grok.Models()) || responses.StringField(catalog.Models[len(catalog.Models)-1], "marker") != tc.marker {
				t.Fatal("selected incorrect v2 template")
			}
		})
	}
}

func TestGrokCatalogRebuildsCachedMetadata(t *testing.T) {
	for _, stale := range []string{"grok:grok-4.6", "grok-unknown", "opencode-go:unknown", "opencode-zen:unknown"} {
		t.Run(stale, func(t *testing.T) {
			body := fmt.Appendf(nil, `{"models":[{"slug":%q,"multi_agent_version":"v2","apply_patch_tool_type":null},{"slug":"gpt-5.6-sol","multi_agent_version":"v2","apply_patch_tool_type":"freeform","shell_type":"unified_exec"}]}`, stale)
			result, err := Build(body, Selection{GrokEnabled: true})
			if err != nil {
				t.Fatal(err)
			}
			var catalog struct {
				Models []map[string]jsonv1.RawMessage `json:"models"`
			}
			if err := json.Unmarshal(result, &catalog); err != nil || len(catalog.Models) != len(grok.Models()) {
				t.Fatalf("rebuilt catalog: %v", err)
			}
			model := catalog.Models[1]
			if responses.StringField(model, "slug") != "grok:grok-4.6" || responses.StringField(model, "apply_patch_tool_type") != "freeform" || responses.StringField(model, "shell_type") != "unified_exec" {
				t.Fatalf("cached Grok tool metadata was not rebuilt: %s", result)
			}
		})
	}
}

func TestOpenCodeCatalog(t *testing.T) {
	config := opencode.Config{Go: opencode.ServiceConfig{APIKey: "go"}, Zen: opencode.ServiceConfig{APIKey: "zen"}}
	body := []byte(`{"models":[{"slug":"gpt-5.6-sol","multi_agent_version":"v2","shell_type":"unified_exec","apply_patch_tool_type":"freeform","model_messages":{"instructions_template":"native"},"unknown_future":42}]}`)
	catalog, err := Build(body, Selection{OpenCode: config})
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Models []map[string]jsontext.Value `json:"models"`
	}
	if err := json.Unmarshal(catalog, &result); err != nil || len(result.Models) != len(config.Services()[0].Models())+len(config.Services()[1].Models()) {
		t.Fatalf("model registration: %v", err)
	}
	var levels []map[string]string
	var foundDescription, foundReasoning bool
	for _, model := range result.Models {
		if string(model["description"]) != `""` {
			foundDescription = true
		}
		levels = nil
		if err := json.Unmarshal(model["supported_reasoning_levels"], &levels); err != nil {
			t.Fatal(err)
		}
		foundReasoning = foundReasoning || len(levels) != 0
		if string(model["shell_type"]) != `"unified_exec"` || string(model["unknown_future"]) != "42" ||
			string(model["use_responses_lite"]) != "false" || string(model["default_reasoning_level"]) != `null` {
			t.Fatalf("inherited incorrect host/provider metadata: %s", responses.MustMarshalJSON(model))
		}
	}
	if !foundDescription || !foundReasoning {
		t.Fatal("embedded provider metadata was not projected")
	}
	again, err := Build(catalog, Selection{OpenCode: config})
	if err != nil || !bytes.Equal(catalog, again) {
		t.Fatalf("catalog not idempotent: %v", err)
	}
	disabled, err := Build(catalog, Selection{GrokEnabled: true})
	if err != nil || bytes.Contains(disabled, []byte("opencode-")) {
		t.Fatalf("stale disabled providers retained: %v", err)
	}
}
