package frontends

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/yusing/mekugi/contracts/command"
)

func TestFrontendGuidanceUsesAuthenticatedDescriptions(t *testing.T) {
	registry := sharedTestRegistry(t)
	guide := registry.FrontendGuidance
	if !strings.Contains(guide, "redirect to a file only when the task needs that artifact, not for display preference") {
		t.Fatal("projected frontend guidance lacks output artifact rule")
	}
	generated, err := os.ReadFile(filepath.Join(registry.SnapshotDir, "frontend_guidance.md"))
	if err != nil || string(generated) != guide || strings.Contains(guide, "{{") || strings.Contains(guide, "<instruction id=") {
		t.Fatalf("standalone generated frontend guidance differs from projection: %v", err)
	}
	byName := make(map[string]catalogEntry, len(registry.ordered))
	for _, contribution := range registry.ordered {
		byName[contribution.Name] = contribution
	}
	for _, name := range []string{"mcat", "inspect_file", "msymbol", "mrun", "mchanges", "mread"} {
		var specification struct {
			Description string `json:"description"`
		}
		if err := json.Unmarshal(byName[name].Specification, &specification); err != nil {
			t.Fatal(err)
		}
		if specification.Description == "" || strings.Count(guide, specification.Description) != 1 {
			t.Fatalf("%s description is not projected exactly once", name)
		}
	}
	for _, removed := range []string{"hcat", "hread", "hpatch", "hchanges", "functions.shell"} {
		if regexp.MustCompile(`\b` + regexp.QuoteMeta(removed) + `\b`).MatchString(guide) {
			t.Fatalf("retired instruction %q in frontend guide", removed)
		}
	}
	changed, err := testGuidance().Frontends([]command.Spec{{Name: "mcat", Description: "updated valid description"}})
	if err != nil || strings.Count(changed, "updated valid description") != 1 {
		t.Fatalf("selected description not rendered: %v", err)
	}
	for _, initial := range []string{"stock tool description", "stock tool description\n" + frontendGuidanceStart + "stale" + frontendGuidanceEnd} {
		first, err := testGuidance().ToolDescription(initial, guide, false)
		if err != nil {
			t.Fatal(err)
		}
		second, err := testGuidance().ToolDescription(first, guide, false)
		if err != nil || first != second || strings.Count(second, frontendGuidanceStart) != 1 {
			t.Fatalf("frontend guidance not stable: %v", err)
		}
	}
	for _, malformed := range []string{frontendGuidanceStart, frontendGuidanceEnd, frontendGuidanceEnd + frontendGuidanceStart, frontendGuidanceStart + frontendGuidanceStart + frontendGuidanceEnd} {
		if _, err := testGuidance().ToolDescription(malformed, guide, false); err == nil {
			t.Fatalf("accepted malformed frontend markers: %q", malformed)
		}
	}
}
