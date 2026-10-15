//go:build unix

package contextreset_test

import (
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/commandanalysis/shellsyntax"
	"github.com/yusing/mekugi/modules/contextreset/guidance"
)

func resetGuidanceTestItem(t *testing.T, fields map[string]any) map[string]jsonv1.RawMessage {
	t.Helper()
	data, err := json.Marshal(&fields)
	if err != nil {
		t.Fatal(err)
	}
	var item map[string]jsonv1.RawMessage
	if err := json.Unmarshal(data, &item); err != nil {
		t.Fatal(err)
	}
	return item
}

func resetGuidanceTestCall(t *testing.T, id, command string) map[string]jsonv1.RawMessage {
	t.Helper()
	arguments, err := json.Marshal(map[string]any{"cmd": command})
	if err != nil {
		t.Fatal(err)
	}
	return resetGuidanceTestItem(t, map[string]any{
		"type": "function_call", "name": "exec_command", "call_id": id,
		"arguments": string(arguments),
	})
}

func resetGuidanceTestOutput(t *testing.T, id string) map[string]jsonv1.RawMessage {
	t.Helper()
	return resetGuidanceTestItem(t, map[string]any{"type": "function_call_output", "call_id": id, "output": "read complete"})
}

func resetGuidanceTestFile(t *testing.T, path, body string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResetGuidanceEligibleReads(t *testing.T) {
	workspace := t.TempDir()
	path := resetGuidanceTestFile(t, filepath.Join(workspace, "POLICY.md"), "Whole source, including the final section.\n")
	for _, tc := range []struct {
		name, command          string
		completed, named, want bool
	}{
		{"literal", "cat POLICY.md", true, true, true},
		{"range selects whole source", "mcat --number POLICY.md 1:1", true, true, true},
		{"inspection selects source", "inspect_file POLICY.md", true, true, true},
		{"uncompleted", "cat POLICY.md", false, true, false},
		{"unnamed", "cat POLICY.md", true, false, false},
		{"different path token", "cat POLICY.md", true, false, false},
		{"dynamic variable", `cat "$SOURCE"`, true, true, false},
		{"substitution", "cat $(printf POLICY.md)", true, true, false},
		{"pipeline", "cat POLICY.md | head", true, true, false},
		{"redirect", "cat POLICY.md > output", true, true, false},
		{"background", "cat POLICY.md &", true, true, false},
		{"assignment", "MODE=read cat POLICY.md", true, true, false},
		{"unsupported reader", "head POLICY.md", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items := []map[string]jsonv1.RawMessage{resetGuidanceTestCall(t, "read", tc.command)}
			if tc.completed {
				items = append(items, resetGuidanceTestOutput(t, "read"))
			}
			base := ""
			if tc.named {
				base = "Read POLICY.md."
			} else if tc.name == "different path token" {
				base = "Read POLICY.md.backup."
			}
			got := collectResetGuidance(t, items, base, workspace, nil, nil)
			if !tc.want {
				if got != nil {
					t.Fatalf("ineligible read collected: %+v", got)
				}
				return
			}
			wantText := guidance.Header + "\nFile: " + path + "\nWhole source, including the final section.\n"
			if got == nil || got.Text != wantText || !slices.Equal(got.Commands, []string{"cat " + shellsyntax.QuoteArgument(path)}) || got.Tool != "function_call" || got.Name != "exec_command" {
				t.Fatalf("snapshot = %+v, want whole eligible source", got)
			}
		})
	}
	if got := collectResetGuidance(t, nil, "Read POLICY.md.", ".", nil, nil); got != nil {
		t.Fatalf("relative workspace collected guidance: %+v", got)
	}
}

func TestResetGuidanceEarlyCompletedRounds(t *testing.T) {
	workspace := t.TempDir()
	var items []map[string]jsonv1.RawMessage
	var paths []string
	for i := 1; i <= 6; i++ {
		path := resetGuidanceTestFile(t, filepath.Join(workspace, fmt.Sprintf("round%d.md", i)), fmt.Sprintf("Round %d policy.\n", i))
		paths = append(paths, path)
		id := fmt.Sprintf("read%d", i)
		items = append(items, resetGuidanceTestCall(t, id, "cat "+shellsyntax.QuoteArgument(path)), resetGuidanceTestOutput(t, id))
	}
	got := collectResetGuidance(t, items, strings.Join(paths, "\n"), workspace, nil, nil)
	if got == nil || len(got.Commands) != 5 || !slices.Equal(got.FirstReadOrder, paths[:5]) || strings.Contains(got.Text, "Round 6 policy") {
		t.Fatalf("early rounds = %+v", got)
	}
	for i := 1; i <= 5; i++ {
		if !strings.Contains(got.Text, fmt.Sprintf("Round %d policy.", i)) {
			t.Errorf("missing round %d", i)
		}
	}
}

func TestResetGuidanceSourceOrderAndStructure(t *testing.T) {
	workspace := t.TempDir()
	globalRoot := t.TempDir()
	t.Setenv("HOME", globalRoot)
	global := resetGuidanceTestFile(t, filepath.Join(globalRoot, ".codex", "GLOBAL.md"), "Global policy.\n")
	first := resetGuidanceTestFile(t, filepath.Join(workspace, "FIRST.md"), "First policy.\n")
	second := resetGuidanceTestFile(t, filepath.Join(workspace, "SECOND.md"), "Second policy.\n")
	nested := resetGuidanceTestFile(t, filepath.Join(workspace, "pkg", "policy.go"), "package policy\n\nfunc Check() string { return \"private implementation\" }\n")
	project := resetGuidanceTestItem(t, map[string]any{"role": "user", "content": "# AGENTS.md instructions for " + workspace + "\nRead FIRST.md, SECOND.md, and pkg/policy.go."})
	custom := resetGuidanceTestItem(t, map[string]any{"type": "custom_tool_call", "name": "exec", "call_id": "batch", "input": `text(await tools.exec_command({cmd: "mcat SECOND.md 1:1 FIRST.md 1:1; inspect_file pkg/policy.go"}));`})
	items := []map[string]jsonv1.RawMessage{project, resetGuidanceTestCall(t, "global", `cat "$HOME/.codex/GLOBAL.md"`), custom,
		resetGuidanceTestOutput(t, "global"), resetGuidanceTestItem(t, map[string]any{"type": "custom_tool_call_output", "call_id": "batch", "output": "read complete"})}
	order := []string{nested, first, second, global}
	got := collectResetGuidance(t, items, "In ~/.codex read GLOBAL.md.", workspace, order, nil)
	wantCommands := []string{"cat " + shellsyntax.QuoteArgument(global), "cat " + shellsyntax.QuoteArgument(first), "cat " + shellsyntax.QuoteArgument(second), "inspect_file " + shellsyntax.QuoteArgument(nested)}
	if got == nil || !slices.Equal(got.Commands, wantCommands) || !slices.Equal(got.FirstReadOrder, order) {
		t.Fatalf("ordered guidance = %+v, want commands %q", got, wantCommands)
	}
	if !strings.Contains(got.Text, "\nStructure: "+nested+"\n3-3 function Check\n") || strings.Contains(got.Text, "private implementation") {
		t.Fatalf("nested source must use actual outline: %s", got.Text)
	}
}

func TestResetGuidanceUnavailableSources(t *testing.T) {
	workspace := t.TempDir()
	missing := filepath.Join(workspace, "missing.md")
	directory := filepath.Join(workspace, "directory.md")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	invalid := resetGuidanceTestFile(t, filepath.Join(workspace, "invalid.md"), string([]byte{0xff, 0xfe}))
	target := resetGuidanceTestFile(t, filepath.Join(workspace, "secret.md"), "Unselected target content.\n")
	link := filepath.Join(workspace, "linked.md")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ path, reason string }{
		{missing, "Unavailable:"}, {directory, "not a regular file"}, {invalid, "not UTF-8"}, {link, "resolved path is not instruction-named"},
	} {
		t.Run(filepath.Base(tc.path), func(t *testing.T) {
			items := []map[string]jsonv1.RawMessage{resetGuidanceTestCall(t, "read", "cat "+shellsyntax.QuoteArgument(tc.path)), resetGuidanceTestOutput(t, "read")}
			got := collectResetGuidance(t, items, "Read "+tc.path, workspace, nil, nil)
			if got == nil || len(got.Commands) != 0 || got.Name != "" || got.Tool != "" || !strings.Contains(got.Text, "Unavailable: cat "+shellsyntax.QuoteArgument(tc.path)) || !strings.Contains(got.Text, tc.reason) || strings.Contains(got.Text, "Unselected target content") {
				t.Fatalf("unavailable-only result = %+v", got)
			}
		})
	}
}

func TestResetGuidanceFileBudget(t *testing.T) {
	workspace := t.TempDir()
	large := strings.Repeat("alpha beta gamma delta\n", 4000)
	largePath := resetGuidanceTestFile(t, filepath.Join(workspace, "LARGE.md"), large)
	smallPath := resetGuidanceTestFile(t, filepath.Join(workspace, "SMALL.md"), "Retained small policy.\n")
	items := []map[string]jsonv1.RawMessage{resetGuidanceTestCall(t, "read", "cat LARGE.md SMALL.md"), resetGuidanceTestOutput(t, "read")}
	got := collectResetGuidance(t, items, "Read LARGE.md and SMALL.md.", workspace, nil, nil)
	if got == nil || !slices.Equal(got.Commands, []string{"cat " + shellsyntax.QuoteArgument(smallPath)}) || !strings.Contains(got.Text, "Retained small policy.") || !strings.Contains(got.Text, "Omitted by budget: cat "+shellsyntax.QuoteArgument(largePath)) {
		t.Fatalf("budget must skip oversized source and retain later source: %+v", got)
	}
}
