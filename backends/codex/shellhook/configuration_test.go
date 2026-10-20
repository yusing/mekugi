package shellhook

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"reflect"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestHookConfigPreservesCallerConfiguration(t *testing.T) {
	// Empty groups and handler positions are part of native hook keys.
	prior := Invocation{
		PreToolUse: []jsontext.Value{
			jsontext.Value(`{"matcher":"Read","hooks":[]}`),
			jsontext.Value(`{"matcher":"*","hooks":[{"type":"command","command":"printf '\"quoted\" ☃ \ud83d\ude00 \u007f'\n","timeout":9007199254740993,"async":true},{"type":"command","command":"caller","timeout":1.25,"async":false}]}`),
		},
		State: map[string]jsontext.Value{
			"recovery\x7f":     jsontext.Value(`{"trusted_hash":"sha256:recovery","enabled":true}`),
			"caller:1:1":       jsontext.Value(`{"trusted_hash":"sha256:caller","enabled":true,"note":"keep"}`),
			"already-disabled": jsontext.Value(`{"enabled":false}`),
		},
	}
	before, err := json.Marshal(prior, json.Deterministic(true))
	if err != nil {
		t.Fatal(err)
	}
	config, state, hash, err := Config("/helper's path", "/guard", "/sudo", "/tracker", prior, []string{"caller:1:1", "file:0:0"})
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Hooks struct {
			PreToolUse []map[string]any
			State      map[string]map[string]any
		}
	}
	if _, err := toml.Decode(config+"\n"+state, &parsed); err != nil {
		t.Fatalf("invalid override TOML: %v", err)
	}
	groups := parsed.Hooks.PreToolUse
	if len(groups) != 3 || len(groups[0]["hooks"].([]any)) != 0 || groups[0]["matcher"] != "Read" {
		t.Fatalf("changed group positions: %#v", groups)
	}
	caller := groups[1]["hooks"].([]any)
	if len(caller) != 2 {
		t.Fatalf("changed caller handlers: %#v", caller)
	}
	first, second := caller[0].(map[string]any), caller[1].(map[string]any)
	if first["timeout"] != int64(9007199254740993) || first["command"] != "printf '\"quoted\" ☃ 😀 \x7f'\n" || first["async"] != true || second["timeout"] != 1.25 || second["command"] != "caller" {
		t.Fatalf("changed caller values: %#v", caller)
	}
	wantState := map[string]map[string]any{
		"recovery\x7f":     {"trusted_hash": "sha256:recovery", "enabled": true},
		"caller:1:1":       {"trusted_hash": "sha256:caller", "enabled": false, "note": "keep"},
		"already-disabled": {"enabled": false},
		"file:0:0":         {"enabled": false},
		"/<session-flags>/config.toml:pre_tool_use:2:0": {"trusted_hash": hash, "enabled": true},
	}
	if !reflect.DeepEqual(parsed.Hooks.State, wantState) {
		t.Fatalf("state=%#v, want %#v", parsed.Hooks.State, wantState)
	}
	after, err := json.Marshal(prior, json.Deterministic(true))
	if err != nil || string(after) != string(before) {
		t.Fatalf("mutated caller configuration: before=%s after=%s err=%v", before, after, err)
	}
	guard := groups[2]["hooks"].([]any)
	if len(guard) != 1 || guard[0].(map[string]any)["statusMessage"] != "Applying shell guard" {
		t.Fatalf("unexpected guard handler: %#v", guard)
	}
	// Authenticate the actual TOML-decoded handler, not a second Config call.
	identity := map[string]any{"event_name": "pre_tool_use", "matcher": groups[2]["matcher"], "hooks": groups[2]["hooks"]}
	encoded, err := json.Marshal(identity, json.Deterministic(true))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	if expected := "sha256:" + hex.EncodeToString(digest[:]); hash != expected {
		t.Fatalf("hash=%s want=%s", hash, expected)
	}
	entry := guardHookTestEntry(hash)
	entry["key"] = "/<session-flags>/config.toml:pre_tool_use:2:0"
	if err := Validate(guardHookTestResult(t, "/workspace", entry), "/workspace", hash); err != nil {
		t.Fatal(err)
	}
}

func TestHookConfigRejectsUnrepresentableCallerValues(t *testing.T) {
	for _, prior := range []Invocation{
		{PreToolUse: []jsontext.Value{jsontext.Value(`{"hooks":null}`)}},
		{State: map[string]jsontext.Value{"conflict": jsontext.Value(`false`)}},
	} {
		if _, _, _, err := Config("/helper", "/guard", "", "", prior, []string{"conflict"}); err == nil {
			t.Fatal("accepted invalid caller hook configuration")
		}
	}
}
