package router

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func assertNativeStaticGuidance(t *testing.T, packet map[string]any, plugin string, registry *toolRegistry) {
	t.Helper()
	skill, err := os.ReadFile(filepath.Join(plugin, "skills", "mekugi", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, body, ok := strings.Cut(string(skill), "\n---\n")
	if !ok {
		t.Fatal("generated workflow has no body")
	}
	body = strings.TrimSpace(body)
	context := nativeGuidanceRequestText(packet["messages"])
	if strings.Count(context, body) != 1 || strings.Contains(nativeGuidanceRequestText(packet["system"]), body) {
		t.Fatal("mod did not deliver exactly one complete current workflow without preset append")
	}
	assertNativeFrontendContracts(t, registry, context)
}

func assertNativeCompanionTools(t *testing.T, packet map[string]any) {
	t.Helper()
	names := make(map[string]bool)
	tools, _ := packet["tools"].([]any)
	for _, value := range tools {
		tool, _ := value.(map[string]any)
		name, _ := tool["name"].(string)
		names[name] = true
	}
	for _, name := range []string{"mcp__mekugi__journal_batch", "mcp__mekugi__journal_read", "mcp__mekugi__mchanges"} {
		if !names[name] {
			t.Fatalf("native work prompt lacks %s", name)
		}
	}
}

func nativeGuidanceFixtureConfig(t *testing.T) {
	t.Helper()
	directory := t.TempDir()
	// Keep scripted requests on native interactive permission checks. Auto
	// mode requires an additional real classifier, outside this local fixture.
	if err := os.WriteFile(filepath.Join(directory, "settings.json"), []byte(`{"permissions":{"defaultMode":"default"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", directory)
	if err := os.WriteFile(filepath.Join(directory, "skills-mgr"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// These replacements run only in the scripted-provider guidance fixtures.
// They prove the native consumers, without changing production prompt policy.
func installNativePromptModFixture(t *testing.T, plugin string) {
	t.Helper()
	hooks := filepath.Join(plugin, "hooks")
	if err := os.MkdirAll(hooks, 0700); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(filepath.Join(hooks, "register.js"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooks, "guidance.js"), original, 0600); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"hooks.json": `{"modules":["./register.js"]}`,
		"register.js": `import {register as guidance} from './guidance.js';
export function register(on) {
  guidance(on);
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

func assertNativeManagedSkillFixture(t *testing.T, packet map[string]any, managed bool) {
	t.Helper()
	tools, _ := packet["tools"].([]any)
	found := false
	for _, value := range tools {
		tool, _ := value.(map[string]any)
		found = found || tool["name"] == "Skill"
	}
	if found == managed {
		t.Fatalf("native Skill availability differs from selected owner: managed=%t offered=%t", managed, found)
	}
}
