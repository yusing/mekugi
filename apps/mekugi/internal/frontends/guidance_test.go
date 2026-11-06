package frontends

import (
	"crypto/sha256"
	"encoding/hex"
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/contracts/command"
)

func TestFrontendGuidanceUsesAuthenticatedDescriptions(t *testing.T) {
	registry := sharedTestRegistry(t)
	guide := registry.FrontendGuidance.Descriptions
	if !strings.Contains(registry.FrontendGuidance.Workflow, "redirect to a file only when the task needs that artifact, not for display preference") {
		t.Fatal("projected frontend guidance lacks output artifact rule")
	}
	saved := registry.FrontendGuidance
	if strings.Contains(guide, "{{") || strings.Contains(guide, "<instruction id=") {
		t.Fatal("unrendered frontend guidance")
	}
	generated, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(registry.SnapshotDir, "frontend_guidance.json")); !os.IsNotExist(err) {
		t.Fatalf("unused guidance snapshot exists: %v", err)
	}
	for _, codeOnly := range []string{"tools.exec_command", "Pass `shell:", "exec output", "write_stdin"} {
		if strings.Contains(saved.Shell, codeOnly) {
			t.Fatalf("native shell guidance contains Codex execution instruction %q", codeOnly)
		}
	}
	if !strings.Contains(saved.Workflow, "The user can see exec output") || !strings.Contains(saved.Shell, "follow its next_call") || !strings.Contains(saved.Shell, "Reuse completed edit-receipt IDs") {
		t.Fatal("frontend projections lost execution or shared helper workflow")
	}
	for _, projection := range []string{saved.Workflow, saved.Shell} {
		if !strings.Contains(projection, "Display labels are not recovery handles") {
			t.Fatal("frontend projection lost recovery-handle distinction")
		}
	}
	for _, instruction := range []string{"Print raw output once", "within the outer exec budget", "overflow can erase next_call handles"} {
		if !strings.Contains(saved.Workflow, instruction) || strings.Contains(saved.Shell, instruction) {
			t.Fatalf("exec output guidance must be confined to Codex: %q", instruction)
		}
	}
	manifest, err := readToolWorkerManifest(filepath.Join(registry.SnapshotDir, manifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(generated)
	if manifest.GuidanceSHA256 != hex.EncodeToString(digest[:]) {
		t.Fatal("snapshot sections are not authenticated")
	}
	for _, contribution := range testCommands() {
		if !slices.Contains(manifest.Tools, contribution.Name) || strings.Count(guide, contribution.Description) != 1 || strings.Count(saved.Shell, contribution.Description) != 1 {
			t.Fatalf("%s description is not projected exactly once", contribution.Name)
		}
	}
	for _, removed := range []string{"hcat", "hread", "hpatch", "hchanges", "functions.shell"} {
		if regexp.MustCompile(`\b` + regexp.QuoteMeta(removed) + `\b`).MatchString(guide) {
			t.Fatalf("retired instruction %q in frontend guide", removed)
		}
	}
	changed, err := testGuidance().Frontends([]command.Spec{{Name: "mcat", Description: "updated valid description"}})
	if err != nil || strings.Count(changed.Descriptions, "updated valid description") != 1 {
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
