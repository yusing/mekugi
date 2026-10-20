//go:build unix

package contextreset_test

import (
	"context"
	jsonv1 "encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/contextreset"
	"github.com/yusing/mekugi/modules/contextreset/codexbinding"
	"github.com/yusing/mekugi/modules/contextreset/guidance"
	"github.com/yusing/mekugi/modules/fileread"
	"github.com/yusing/mekugi/modules/sourceoutline"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

func provenanceService(t *testing.T, readSkill guidance.ReadSkill) guidance.Service {
	t.Helper()
	script := &moduletest.Scripted{}
	return moduletest.New(t, contextreset.Module, contextreset.Deps{
		Reader:    moduletest.New(t, fileread.Module, fileread.Deps{}, module.Slots{}, script),
		Outline:   moduletest.New(t, sourceoutline.Module, sourceoutline.Deps{}, module.Slots{}, script),
		ReadSkill: readSkill,
	}, module.Slots{}, script)
}

func TestResetGuidanceRecoveryProvenance(t *testing.T) {
	workspace := t.TempDir()
	for _, name := range []string{"FIRST.md", "SECOND.md", "LATE.md", "RESTORED.md"} {
		resetGuidanceTestFile(t, filepath.Join(workspace, name), name+" contents\n")
	}
	var items []map[string]jsonv1.RawMessage
	for i := range 4 {
		items = append(items, resetGuidanceTestItem(t, map[string]any{"type": "reasoning"}), resetGuidanceTestOutput(t, fmt.Sprint(i)))
	}
	message := func(role, text string) map[string]jsonv1.RawMessage {
		return resetGuidanceTestItem(t, map[string]any{"type": "message", "role": role, "content": []map[string]string{{"type": "output_text", "text": text}}})
	}
	items = append(items,
		message("assistant", "Recovered summary."), message("user", "Continue."),
		message("assistant", "Previously restored guidance."),
		resetGuidanceTestCall(t, "call_mekugi_guidance_old", "true"),
		resetGuidanceTestOutput(t, "call_mekugi_guidance_old"), message("user", "Continue."),
		resetGuidanceTestCall(t, "fifth-a", "cat FIRST.md"),
		// A restored output inside a response must not split its parallel calls.
		resetGuidanceTestOutput(t, "call_mekugi_guidance_old"),
		resetGuidanceTestCall(t, "fifth-b", "cat SECOND.md"),
		resetGuidanceTestOutput(t, "fifth-a"), resetGuidanceTestOutput(t, "fifth-b"),
		resetGuidanceTestCall(t, "sixth", "cat LATE.md"), resetGuidanceTestOutput(t, "sixth"),
		resetGuidanceTestCall(t, "call_mekugi_guidance_late", "cat RESTORED.md"),
		resetGuidanceTestOutput(t, "call_mekugi_guidance_late"),
	)
	service := provenanceService(t, func(context.Context, string, string, string) ([]byte, bool, error) {
		t.Fatal("unexpected skill acquisition")
		return nil, false, nil
	})
	got := service.Collect(t.Context(), codexbinding.Decode(items, "Read FIRST.md SECOND.md LATE.md RESTORED.md.", workspace, nil, nil, "Recovered summary."))
	if got == nil || len(got.Commands) != 3 {
		t.Fatalf("recovery collection = %+v", got)
	}
	for _, name := range []string{"FIRST.md", "SECOND.md", "RESTORED.md"} {
		if !strings.Contains(got.Text, name+" contents") {
			t.Errorf("missing %s contents: %s", name, got.Text)
		}
	}
	if strings.Contains(got.Text, "LATE.md contents") {
		t.Fatalf("sixth response collected: %s", got.Text)
	}
}

func TestResetGuidanceRepeatedCallCompletion(t *testing.T) {
	workspace := t.TempDir()
	first := resetGuidanceTestFile(t, filepath.Join(workspace, "FIRST.md"), "First occurrence.\n")
	second := resetGuidanceTestFile(t, filepath.Join(workspace, "SECOND.md"), "Repeated occurrence.\n")
	items := []map[string]jsonv1.RawMessage{resetGuidanceTestCall(t, "repeated", "cat FIRST.md")}
	for range 5 {
		items = append(items, resetGuidanceTestItem(t, map[string]any{"role": "user", "content": "Continue."}), resetGuidanceTestItem(t, map[string]any{"type": "reasoning"}))
	}
	// Completion is joined across the entire history, while the repeated call
	// retains its earliest qualifying response rather than its latest response.
	items = append(items, resetGuidanceTestCall(t, "repeated", "cat SECOND.md"), resetGuidanceTestOutput(t, "repeated"))
	got := collectResetGuidance(t, items, "Read FIRST.md SECOND.md.", workspace, nil, nil)
	if got == nil || !slices.Equal(got.FirstReadOrder, []string{first, second}) || !strings.Contains(got.Text, "First occurrence.") || !strings.Contains(got.Text, "Repeated occurrence.") {
		t.Fatalf("repeated completed call = %+v", got)
	}
}

func TestResetGuidanceLegacySkillIdentity(t *testing.T) {
	const id = "call_mekugi_guidance_legacy"
	items := []map[string]jsonv1.RawMessage{resetGuidanceTestCall(t, id, "skills-mgr get --codex legacy"), resetGuidanceTestOutput(t, id)}
	for _, tc := range []struct {
		name     string
		retained map[string][]guidance.Skill
		want     bool
	}{
		{name: "absent identity uses legacy command", want: true},
		{name: "present empty identity suppresses legacy command", retained: map[string][]guidance.Skill{id: {}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			calls := 0
			service := provenanceService(t, func(_ context.Context, gotWorkspace, name, path string) ([]byte, bool, error) {
				calls++
				if gotWorkspace != workspace || name != "legacy" || path != "" {
					t.Fatalf("skill acquisition = %q, %q, %q", gotWorkspace, name, path)
				}
				return []byte("Current managed skill content.\n"), true, nil
			})
			got := service.Collect(t.Context(), codexbinding.Decode(items, "", workspace, nil, tc.retained))
			if !tc.want {
				if got != nil || calls != 0 {
					t.Fatalf("empty retained identity acquired %d skills: %+v", calls, got)
				}
				return
			}
			if got == nil || calls != 1 || !strings.Contains(got.Text, "Current managed skill content.") || !slices.Equal(got.Skills, []guidance.Skill{{Name: "legacy"}}) || !slices.Equal(got.Commands, []string{"skills-mgr get --codex legacy"}) {
				t.Fatalf("legacy restored skill = %+v, acquisitions = %d", got, calls)
			}
		})
	}
}

func TestResetGuidanceNilAndEmptyReadOrder(t *testing.T) {
	workspace := t.TempDir()
	path := resetGuidanceTestFile(t, filepath.Join(workspace, "POLICY.md"), "Retained policy.\n")
	items := []map[string]jsonv1.RawMessage{resetGuidanceTestCall(t, "read", "cat POLICY.md"), resetGuidanceTestOutput(t, "read")}
	for _, tc := range []struct {
		name  string
		order []string
	}{
		{name: "nil infers first reads"},
		{name: "empty preserves supplied provenance", order: []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := collectResetGuidance(t, items, "Read POLICY.md.", workspace, tc.order, nil)
			if got == nil || !strings.Contains(got.Text, "Retained policy.") {
				t.Fatalf("eligible source lost: %+v", got)
			}
			want := tc.order
			if want == nil {
				want = []string{path}
			}
			if got.FirstReadOrder == nil || !slices.Equal(got.FirstReadOrder, want) {
				t.Fatalf("first read order = %#v, want %#v", got.FirstReadOrder, want)
			}
		})
	}
}
