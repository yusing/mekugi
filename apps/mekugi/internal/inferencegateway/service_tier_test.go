package inferencegateway

import (
	"github.com/yusing/mekugi/providers/opencode"

	"testing"
)

func TestTierChoicesKeepInvocationDefaults(t *testing.T) {
	configured := map[string]string{"gpt-6-sol": "fast"}
	tiers := NewTiers(configured)
	configured["gpt-6-sol"] = "flex"
	tiers.Confirm("gpt-5.6-terra", "main", "")
	if tiers.Effective("gpt-6-sol", "flex", "main") != "default" || tiers.Effective("gpt-5.6-terra", "default", "child") != "priority" {
		t.Fatal("choice escaped its thread/model or changed invocation defaults")
	}
	if tier, ok := tiers.Choice("gpt-5.6-terra", "main"); !ok || tier != "default" {
		t.Fatal("legacy selection did not share routed-model identity")
	}
}

func TestServiceTierSurvivesProviderTranslation(t *testing.T) {
	for _, service := range []opencode.Service{{}, {Prefix: "opencode-go"}, {Prefix: "opencode-zen"}} {
		if service.Prefix == "" {
			tr, err := testTranslateProviderRequest([]byte(`{"model":"grok:grok-4.6","service_tier":"fast","input":[]}`), nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := string(mustTestJSON(t, tr.body["service_tier"])); got != `"fast"` {
				t.Fatalf("Grok tier=%s", got)
			}
			continue
		}
		formats := make(map[string]bool)
		for _, model := range service.Models() {
			format := service.Format(model.ID)
			if formats[format] {
				continue
			}
			formats[format] = true
			t.Run(service.Prefix+"/"+format, func(t *testing.T) {
				body := mustTestJSON(t, map[string]any{"model": service.Prefix + ":" + model.ID, "service_tier": "fast", "input": []any{}})
				tr, err := testTranslateProviderRequest(body, &service)
				if err != nil {
					t.Fatal(err)
				}
				if got := string(mustTestJSON(t, tr.body["service_tier"])); got != `"fast"` {
					t.Fatalf("tier=%s", got)
				}
			})
		}
	}
}
