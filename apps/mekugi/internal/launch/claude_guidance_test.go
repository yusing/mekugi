package launch

import (
	json "encoding/json/v2"
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/agentguidance"
	"github.com/yusing/mekugi/testsupport/moduletest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/modules/agentguidance/compact"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestClaudeGuidanceAndPreload(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "child")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{
		filepath.Join(parent, "reviewer.toml"):     "model = 'codex-pinned'\nmodel_reasoning_effort = 'high'\n",
		filepath.Join(parent, "AGENTS.md"):         "parent rules",
		filepath.Join(child, "AGENTS.md"):          "superseded rules",
		filepath.Join(child, "AGENTS.override.md"): "child rules",
		filepath.Join(child, "source.txt"):         "first\nsecond\nthird\n",
	} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	preparation, err := json.Marshal(map[string]any{"layers": []any{map[string]any{
		"name": map[string]any{"file": filepath.Join(parent, "config.toml")},
		"config": map[string]any{"agents": map[string]any{"reviewer": map[string]any{
			"description": "Review the implementation.", "config_file": "reviewer.toml",
		}}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := claudeSystemPrompt(control.Settings{Workspace: child, DeveloperInstructions: new("frozen role"), Preparation: preparation}, control.Identity{Thread: "native-id"})
	var value map[string]string
	if err != nil || json.Unmarshal(prompt, &value) != nil {
		t.Fatalf("prompt: %s, %v", prompt, err)
	}
	if value["preset"] != "claude_code" || !strings.Contains(value["append"], "frozen role") || strings.Contains(value["append"], "superseded rules") || strings.Contains(value["append"], "parent rules") || strings.Contains(value["append"], "child rules") {
		t.Fatalf("native guidance lost captured instructions or bypassed native project discovery: %s", prompt)
	}
	if !strings.Contains(value["append"], "reviewer: Review the implementation. (model: codex-pinned, role-selected) (model_reasoning_effort: high, role-selected)") {
		t.Fatalf("native guidance omitted the selected Codex role catalog: %s", prompt)
	}
	if _, err := claudeSystemPrompt(control.Settings{Preparation: []byte(`{"layers":`)}, control.Identity{}); err == nil {
		t.Fatal("invalid captured role configuration was accepted")
	}
	body, err := claudePreload(t.Context(), `{"sources":[{"path":"source.txt","ranges":[[2,2]]},{"path":"missing.txt"},{"path":"AGENTS.md"}]}`, child, os.Environ(), compact.ChangeReader{})
	if err != nil || !strings.Contains(body, "second\n") || strings.Contains(body, "first\n") || !strings.Contains(body, "missing.txt") || !strings.Contains(body, "exit_code=1") || strings.Contains(body, "superseded rules") {
		t.Fatalf("preload lost cwd, ranges or failures: %q, %v", body, err)
	}
}

func TestContextChangesPreloadCompleteComposition(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.publication.SelectSession(t.Context(), nil, store.retentionLimits(), "reader")
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	store.session = storageSessionIdentity{Thread: session.Thread, Namespace: session.Namespace}
	authored, caller := t.TempDir(), t.TempDir()
	path := filepath.Join(authored, "captured.txt")
	final := strings.Repeat("complete retained context beyond tool output limits ", 5000)
	liveDiffScopeCapture(t, store, authored, "author", "first", path, "initial", "intermediate")
	liveDiffScopeCapture(t, store, authored, "author", "second", path, "intermediate", final)
	selected := store.scoped(t.Context()).changeScope()
	index, err := store.changes.Inspect(selected, authored)
	if err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]string)
	for id, change := range index.Changes {
		ids[change.Correlation] = id
	}
	first, second := ids["first"], ids["second"]
	if first == "" || second == "" {
		t.Fatalf("missing captures: %+v", index)
	}
	selection, _ := json.Marshal(map[string]any{"sources": []compact.Source{
		{Changes: []string{second, first}},
		{Changes: []string{first + ".." + second}},
		{Changes: []string{first + "..100"}},
		{Changes: []string{"apply", first}},
		{Changes: []string{first}, Path: "selected.txt"},
		{Path: "selected.txt"},
	}})
	if err := os.WriteFile(filepath.Join(caller, "selected.txt"), []byte("independent file"), 0600); err != nil {
		t.Fatal(err)
	}
	// The authored workspace is absent; selected reads use retained evidence only.
	if err := os.Remove(authored); err != nil {
		t.Fatal(err)
	}
	t.Chdir(caller)
	service := moduletest.New(t, agentguidance.Module, agentguidance.Deps{History: testHistory(), Changes: store.changes}, module.Slots{}, &moduletest.Scripted{})
	output, err := agentguidance.Module.Contributes.Commands[0].Run(service, t.Context(), command.Request{
		Args: []string{string(selection)}, Scope: scope.Retained{Directory: selected.Store.Directory(), Namespace: selected.Namespace, Thread: selected.Thread},
	})
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := output.Delivery.Prepare([]string{"snapshot"})
	if err != nil {
		t.Fatal(err)
	}
	var snapshot compact.Snapshot
	if err := json.Unmarshal([]byte(delivery.Recovery[0].Stdout), &snapshot); err != nil {
		t.Fatal(err)
	}
	sources := snapshot.Sources
	if len(sources) != 6 {
		t.Fatalf("source count: %d", len(sources))
	}
	for _, source := range sources[:2] {
		if source.ExitCode != 0 || !strings.Contains(source.Stdout, "+"+final+"\n") || !strings.Contains(source.Stdout, "-initial\n") || strings.Contains(source.Stdout, "intermediate") {
			t.Fatalf("complete composition lost: exit=%d length=%d diagnostics=%s", source.ExitCode, len(source.Stdout), source.Stderr)
		}
	}
	if sources[0].Stdout != sources[1].Stdout || strings.Contains(delivery.Referenced.Stdout, final) {
		t.Fatal("range composition differs or body entered capped output")
	}
	if sources[2].ExitCode == 0 || !strings.Contains(sources[2].Stdout, final) || sources[2].Stderr == "" {
		t.Fatal("partial range lost available evidence or diagnostic")
	}
	for _, source := range sources[3:5] {
		if source.ExitCode == 0 || source.Stderr == "" {
			t.Fatal("invalid source accepted")
		}
	}
	if sources[5].ExitCode != 0 || sources[5].Stdout != "independent file" {
		t.Fatal("failed changes source suppressed independent file")
	}
	reader := compact.ChangeReader{Service: store.changes, Scope: selected}
	claude, err := claudePreload(t.Context(), string(selection), caller, os.Environ(), reader)
	if err != nil || !strings.Contains(claude, final) || !strings.Contains(claude, "mchanges:"+second+" "+first) {
		t.Fatalf("Claude context lost change source: %v", err)
	}
	liveDiffScopeCapture(t, store, authored, "author", "inconsistent", path, "unwitnessed", "later")
	latest, err := store.changes.Inspect(selected, authored)
	if err != nil {
		t.Fatal(err)
	}
	for id, change := range latest.Changes {
		if change.Correlation == "inconsistent" {
			inconsistent, _ := json.Marshal(map[string]any{"sources": []compact.Source{{Changes: []string{first, second, id}}}})
			captured, err := compact.ReadSources(t.Context(), string(inconsistent), caller, os.Environ(), reader)
			if err != nil || captured.Sources[0].ExitCode == 0 || !strings.Contains(captured.Sources[0].Stderr, "cannot be composed") {
				t.Fatalf("inconsistent capture: exit=%d diagnostics=%s err=%v", captured.Sources[0].ExitCode, captured.Sources[0].Stderr, err)
			}
		}
	}
	reader.Scope.Namespace = "foreign"
	isolated, err := compact.ReadSources(t.Context(), string(selection), caller, os.Environ(), reader)
	if err != nil || isolated.Sources[0].ExitCode == 0 || strings.Contains(isolated.Sources[0].Stdout, final) {
		t.Fatalf("foreign namespace exposed capture: %v", err)
	}
}

func TestClaudeGuidanceSettingsHookEmitsExactContext(t *testing.T) {
	const guidance = "use mchanges 'apple1..apple3' and 100% of \"output\"\n$HOME `pwd`"
	raw, err := claudeGuidanceSettings(guidance)
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Hooks struct {
			SessionStart []struct {
				Matcher string
				Hooks   []struct{ Type, Command string }
			}
		}
	}
	if err := json.Unmarshal(raw, &settings, json.MatchCaseInsensitiveNames(true)); err != nil || len(settings.Hooks.SessionStart) != 1 || settings.Hooks.SessionStart[0].Matcher != "" || len(settings.Hooks.SessionStart[0].Hooks) != 1 {
		t.Fatalf("SessionStart settings: %s, %v", raw, err)
	}
	output, err := exec.CommandContext(t.Context(), "bash", "-c", settings.Hooks.SessionStart[0].Hooks[0].Command).Output()
	if err != nil {
		t.Fatal(err)
	}
	var hook struct {
		Specific struct{ HookEventName, AdditionalContext string } `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(output, &hook, json.MatchCaseInsensitiveNames(true)); err != nil || hook.Specific.HookEventName != "SessionStart" || hook.Specific.AdditionalContext != guidance {
		t.Fatalf("hook output %q: %v", output, err)
	}
}
