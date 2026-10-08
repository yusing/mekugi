package router

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func nativeGuidanceFixtureConfig(t *testing.T) {
	t.Helper()
	directory := t.TempDir()
	// Keep scripted requests on native interactive permission checks. Auto
	// mode requires an additional real classifier, outside this local fixture.
	if err := os.WriteFile(filepath.Join(directory, "settings.json"), []byte(`{"permissions":{"defaultMode":"default"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", directory)
}

// These replacements run only in the scripted-provider guidance fixtures.
// They prove the native consumers, without changing production prompt policy.
func installNativePromptModFixture(t *testing.T, plugin string) {
	t.Helper()
	hooks := filepath.Join(plugin, "hooks")
	if err := os.MkdirAll(hooks, 0700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"hooks.json": `{"modules":["./register.js"]}`,
		"register.js": `export function register(on) {
  on('prompt.section', {name: 'session_guidance'}, async () => ({text: 'NATIVE_MOD_SECTION_REPLACEMENT'}));
  on('prompt.attachment', {type: 'date'}, async ($, e, next) => e.origin.kind === 'engine'
    ? {text: 'NATIVE_MOD_ATTACHMENT_REPLACEMENT'} : next(e));
  on('tool.describe', {tool: 'Read'}, async () => ({description: 'NATIVE_MOD_TOOL_REPLACEMENT'}));
}
`,
	} {
		if err := os.WriteFile(filepath.Join(hooks, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func assertNativePromptModFixture(t *testing.T, packet map[string]any, preset bool) {
	t.Helper()
	if strings.Contains(nativeGuidanceRequestText(packet["system"]), "NATIVE_MOD_SECTION_REPLACEMENT") != preset {
		t.Fatalf("named section replacement differs from native preset selection: preset=%t", preset)
	}
	if !strings.Contains(nativeGuidanceRequestText(packet["messages"]), "NATIVE_MOD_ATTACHMENT_REPLACEMENT") {
		t.Fatal("engine attachment replacement did not reach the native messages")
	}
	tools, _ := packet["tools"].([]any)
	for _, value := range tools {
		tool, _ := value.(map[string]any)
		if tool["name"] == "Read" {
			if tool["description"] != "NATIVE_MOD_TOOL_REPLACEMENT" {
				t.Fatal("tool description replacement did not reach the native tool schema")
			}
			return
		}
	}
	t.Fatal("native prompt-mod fixture has no Read tool")
}
