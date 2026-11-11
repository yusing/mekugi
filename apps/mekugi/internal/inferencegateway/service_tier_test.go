package inferencegateway

import (
	"testing"
)

func TestTierChoicesKeepInvocationDefaults(t *testing.T) {
	configured := map[string]string{"gpt-6-sol": "fast"}
	tiers := NewTiers(configured)
	configured["gpt-6-sol"] = "flex"
	tiers.Confirm("gpt-5.6-terra", "main", "")
	if tiers.Effective("gpt-6-sol", "flex", "main") != "priority" || tiers.Effective("gpt-5.6-terra", "flex", "child") != "flex" {
		t.Fatal("choice escaped its thread/model or changed invocation defaults")
	}
	if tier, ok := tiers.Choice("gpt-5.6-terra", "main"); !ok || tier != "default" {
		t.Fatal("explicit model choice was not retained")
	}
}
