//go:build unix

package router

import (
	"context"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/commandargs"

	"github.com/yusing/mekugi/libraries/outputbudget/tokenizer"
	"github.com/yusing/mekugi/modules/contextreset/codexbinding"
	"github.com/yusing/mekugi/modules/contextreset/guidance"
	"github.com/yusing/mekugi/modules/fileread/read"
	"github.com/yusing/mekugi/modules/sourceoutline/outline"
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
		"type": "function_call", "name": nativeExecCommandToolName, "call_id": id,
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

// The real application skill reader retains its invocation environment.
func collectResetGuidance(t *testing.T, items []map[string]jsonv1.RawMessage, base, workspace string, firstOrder []string, retained map[string][]guidance.Skill) *guidance.Guidance {
	t.Helper()
	environment := os.Environ()
	service, err := guidance.New(read.Service{}, outline.Service{}, func(ctx context.Context, workspace, name, path string) ([]byte, bool, error) {
		return readSkillAttachment(ctx, workspace, environment, name, path)
	})
	if err != nil {
		t.Fatal(err)
	}
	return service.Collect(t.Context(), codexbinding.Decode(items, base, workspace, firstOrder, retained))
}

func TestResetGuidanceLocalSkillIdentity(t *testing.T) {
	workspace := t.TempDir()
	path := resetGuidanceTestFile(t, filepath.Join(workspace, "skills", "audit", "SKILL.md"), "Current audit procedure.\n")
	skill := guidance.Skill{Name: "audit", Path: path}
	selected := "<skill>\n<name>audit</name>\n<path>" + path + "</path>\nOutdated attached procedure.\n</skill>"
	selection := resetGuidanceTestItem(t, map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": selected}}})
	call := resetGuidanceTestCall(t, "skill-read", "cat "+commandargs.QuoteArgument(path))
	output := resetGuidanceTestOutput(t, "skill-read")
	for _, tc := range []struct {
		name     string
		items    []map[string]jsonv1.RawMessage
		retained map[string][]guidance.Skill
	}{
		{"selected", []map[string]jsonv1.RawMessage{selection}, nil},
		{"retained", []map[string]jsonv1.RawMessage{call, output}, map[string][]guidance.Skill{"skill-read": {skill}}},
		{"deduplicated", []map[string]jsonv1.RawMessage{selection, call, output}, map[string][]guidance.Skill{"skill-read": {skill}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := collectResetGuidance(t, tc.items, "", workspace, nil, tc.retained)
			if got == nil || !slices.Equal(got.Skills, []guidance.Skill{skill}) || !slices.Equal(got.FirstReadOrder, []string{"skill:audit\x00" + path}) || !slices.Equal(got.Commands, []string{"cat " + commandargs.QuoteArgument(path)}) {
				t.Fatalf("local skill identity = %+v", got)
			}
			if !strings.Contains(got.Text, "Current audit procedure.") || strings.Contains(got.Text, "Outdated attached procedure.") {
				t.Fatalf("skill snapshot = %s", got.Text)
			}
		})
	}
}

func TestResetGuidanceSkillAdmissionAndBudget(t *testing.T) {
	workspace := t.TempDir()
	large := strings.Repeat("alpha beta gamma delta\n", 4000)
	codec, err := tokenizer.New()
	if err != nil {
		t.Fatal(err)
	}
	if n, err := codec.Count(large); err != nil || n <= guidance.MaxTokens {
		t.Fatalf("budget fixture tokens = %d, error = %v", n, err)
	}
	for _, tc := range []struct{ name, body, notice string }{
		{"missing", "", "Unavailable:"}, {"over budget", large, "Omitted by budget:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "SKILL.md")
			if tc.body != "" {
				resetGuidanceTestFile(t, path, tc.body)
			}
			skill := guidance.Skill{Name: "audit", Path: path}
			items := []map[string]jsonv1.RawMessage{resetGuidanceTestCall(t, "read", "cat "+commandargs.QuoteArgument(path)), resetGuidanceTestOutput(t, "read")}
			got := collectResetGuidance(t, items, "", workspace, nil, map[string][]guidance.Skill{"read": {skill}})
			if got == nil || len(got.Skills) != 0 || len(got.Commands) != 0 || got.Name != "" || got.Tool != "" || !strings.Contains(got.Text, tc.notice) {
				t.Fatalf("rejected skill advertised as admitted: %+v", got)
			}
			if n, err := codec.Count(got.Text); err != nil || n > guidance.MaxTokens {
				t.Fatalf("collected %d tokens, error = %v", n, err)
			}
		})
	}
}
