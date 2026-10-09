package router

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Compile an isolated authoritative-source overlay with all old static carriers
// empty. Production still uses its unchanged bridge until the mod proves parity.
func nativeStaticGuidanceFixtureBridge(t *testing.T, plugin string) string {
	t.Helper()
	source, err := filepath.Abs("../claude/bridge")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	entries, err := os.ReadDir(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".ts") && name != "package.json" && name != "tsconfig.json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(source, name))
		if err != nil {
			t.Fatal(err)
		}
		if name == "bridge.ts" {
			text := string(data)
			for before, after := range map[string]string{
				"const guidance = endpoint ? companionGuidance(endpoint) : '';":               "const guidance = '';",
				"const frontendChunks = endpoint ? companionFrontendGuidance(endpoint) : [];": "const frontendChunks: string[] = [];",
			} {
				if strings.Count(text, before) != 1 {
					t.Fatal("static-carrier fixture overlay no longer matches its source owner")
				}
				text = strings.Replace(text, before, after, 1)
			}
			data = []byte(text)
		}
		if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(source, "node_modules"), filepath.Join(root, "node_modules")); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "npm", "run", "build", "--prefix", root)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("isolated guidance bridge compile: %v\n%s", err, output)
	}
	path := filepath.Join(plugin, "hooks", "register.js")
	skillPath := filepath.Join(plugin, "skills", "mekugi", "SKILL.md")
	frontendsPath := filepath.Join(plugin, "skills", "mekugi", "frontends.md")
	// This proof preserves native attachment text and reads the same current
	// workflow/catalog owners independently of preset append and classic hooks.
	hook := `  on('prompt.attachment', {type: 'date'}, async ($, e, next) => {
    if (e.origin.kind !== 'engine') return next(e);
    const skill = await $.fs.read(` + strconv.Quote(skillPath) + `);
    const workflow = skill.replace(/^---\r?\n[\s\S]*?\r?\n---\r?\n/, '').trim();
    const contracts = await $.fs.read(` + strconv.Quote(frontendsPath) + `);
    const attachment = await next(e);
    return {text: attachment.text + '\n\n' + workflow + '\n\n' + contracts + '\n\nFrontend recovery: ' + ` + strconv.Quote(frontendsPath) + `};
  });
`
	if err := os.WriteFile(path, []byte("export function register(on) {\n"+hook+"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "dist", "bridge.js")
}

func assertNativeStaticGuidanceFixture(t *testing.T, packet map[string]any, plugin string, registry *toolRegistry) {
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
		t.Fatal("mod did not deliver exactly one complete current workflow independently of preset append")
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
