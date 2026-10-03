package router

import (
	"context"
	_ "embed"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/yusing/mekugi/internal/runtimepath"
)

//go:embed claude_companion_skill.md
var claudeCompanionSkill string

type CompanionPresentation struct {
	Plugin            string
	FrontendDirectory string
	JournalSchema     jsontext.Value
}

// PrepareCompanion installs the shared utilities and journal for this launch.
// It does not change persistent Claude configuration.
func (s *ObservationService) PrepareCompanion(ctx context.Context) (CompanionPresentation, error) {
	var result CompanionPresentation
	s.EnableJournal()
	result.JournalSchema = s.JournalSchema()
	var utilitiesText, journalText string
	data, err := mekugiDataDirectory()
	if err != nil {
		return result, err
	}
	runtimeDir, err := runtimepath.Directory()
	if err != nil {
		return result, err
	}
	registry, err := buildRuntimeToolRegistry(ctx, data, runtimeDir, s.owner.store.directory, runtimeFrontendBinding{
		Runtime: s.owner.runtime, Workspace: s.owner.workspace, Endpoint: ObservationEndpoint{Socket: s.endpoint.Socket, Token: s.frontendToken},
	})
	if err != nil {
		return result, err
	}
	s.registry = registry
	if err := registry.installFrontends(); err != nil {
		return result, err
	}
	result.FrontendDirectory = registry.frontendDirectory
	s.receipts = true
	utilitiesText = "Run the enabled commands through Claude's native Bash, with its normal permissions. Read [the shared frontend contracts](frontends.md) when choosing arguments. Native Bash has no Codex yielded-session or write_stdin protocol.\n\nUse explicit recorded change IDs received from companion hooks or the saved Diff pane. Invocation-wide Bash environment does not prove agent identity: bare mchanges, --mine and --list without IDs are unavailable. --workspace cannot select another workspace. Apply/revert require explicit IDs and execute only in the native Bash process; mread recovers retained output without repeating a mutation. Background capture receipts may appear only in saved Diff."
	journalText = "Use companion MCP journal_batch for a batch of plan/add/set/log/remove operations and journal_read for trees (p, depth, view, and agent when ancestry is proven). Paths are stable sibling ordinals. A batch is atomic; a rejected operation leaves the tree unchanged. Task states are pending, working, done, blocked or dropped; blocked/dropped require reason. Context nodes retain constraints, notes retain established facts, tasks retain actionable work. Update the owning node instead of duplicating or contradicting it.\n\nThe MCP handler joins native tool-use metadata to an exact authenticated hook receipt. Missing caller evidence is a rejection, not root authority. Child journals remain unmounted until native Agent results establish their parent; mounted views are read-only. Use view own for an unbound child. Completing a native turn prepares a work report but does not finish authored tasks. Do not add a journal-only finish call or rewrite the substantive answer. After native compaction, bounded journal evidence may be added to the native summary; this does not avoid summary inference or replace context."
	plugin := filepath.Join(s.directory, "plugin")
	if err := os.MkdirAll(filepath.Join(plugin, ".claude-plugin"), 0700); err != nil {
		return result, err
	}
	skillDir := filepath.Join(plugin, "skills", "mekugi")
	if err := os.MkdirAll(skillDir, 0700); err != nil {
		return result, err
	}
	skill := strings.NewReplacer("{{UTILITIES}}", utilitiesText, "{{JOURNAL}}", journalText).Replace(claudeCompanionSkill)
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skill), 0600); err != nil {
		return result, err
	}
	if err := os.WriteFile(filepath.Join(plugin, ".claude-plugin", "plugin.json"), []byte(`{"name":"mekugi","version":"1.0.0","description":"Invocation-local Mekugi companion"}`), 0600); err != nil {
		return result, err
	}
	if s.registry != nil {
		var guide strings.Builder
		for _, entry := range s.registry.ordered {
			if !entry.Executable || len(entry.Specification) == 0 {
				continue
			}
			var spec struct {
				Description string `json:"description"`
			}
			if err := json.Unmarshal(entry.Specification, &spec); err != nil {
				return result, err
			}
			guide.WriteString("## " + entry.Name + "\n\n" + spec.Description + "\n\n")
		}
		if err := os.WriteFile(filepath.Join(skillDir, "frontends.md"), []byte(guide.String()), 0600); err != nil {
			return result, err
		}
	}
	result.Plugin = plugin
	return result, nil
}

func (s *ObservationService) closeCompanion() error {
	if s.journal != nil {
		s.journal.journals.detachNative(s.journal.sink())
	}
	return errors.Join(s.registry.Close(), os.RemoveAll(filepath.Join(s.directory, "plugin")))
}
