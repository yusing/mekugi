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
	Plugin            string         `json:"plugin,omitempty"`
	FrontendDirectory string         `json:"frontendDirectory,omitempty"`
	ManagedSkills     bool           `json:"managedSkills"`
	JournalSchema     jsontext.Value `json:"journalSchema,omitempty"`
}

// PrepareCompanion installs the shared utilities and journal for this launch.
// It does not change persistent Claude configuration.
func (s *ObservationService) PrepareCompanion(ctx context.Context) (CompanionPresentation, error) {
	s.EnableJournal()
	result, registry, err := s.prepareCompanion(ctx, s.owner.workspace, s.frontendToken, filepath.Join(s.directory, "plugin"))
	if err != nil {
		return result, err
	}
	s.owner.mu.Lock()
	s.registry, s.plugin, s.receipts = registry, result.Plugin, true
	s.owner.mu.Unlock()
	return result, nil
}

func (s *ObservationService) prepareCompanion(ctx context.Context, workspace, token, plugin string) (result CompanionPresentation, registry *toolRegistry, err error) {
	defer func() {
		if err != nil {
			err = errors.Join(err, registry.Close(), os.RemoveAll(plugin))
		}
	}()
	result.JournalSchema = s.JournalSchema()
	var utilitiesText, journalText string
	data, err := mekugiDataDirectory()
	if err != nil {
		return result, registry, err
	}
	runtimeDir, err := runtimepath.Directory()
	if err != nil {
		return result, registry, err
	}
	registry, err = buildRuntimeToolRegistry(ctx, data, runtimeDir, s.owner.store.directory, runtimeFrontendBinding{
		Runtime: s.owner.runtime, Workspace: workspace, Endpoint: ObservationEndpoint{Socket: s.endpoint.Socket, Token: token},
	})
	if err != nil {
		return result, registry, err
	}
	if err := registry.installFrontends(); err != nil {
		return result, registry, err
	}
	result.FrontendDirectory = registry.frontendDirectory
	result.ManagedSkills = skillsManagerInWorkspace(workspace, result.FrontendDirectory)
	utilitiesText = "Run the enabled commands through Claude's native Bash, with its normal permissions. The shared frontend contracts are injected into native context; [their catalog](frontends.md) is a recovery reference. Native Bash has no Codex yielded-session or write_stdin protocol.\n\nUse explicit recorded change IDs received from companion hooks or the saved Diff pane. Use MCP mchanges with args [] or [\"--mine\"] for your own changes, or [\"--list\"] to recover your IDs. This read-only adapter joins the exact native caller receipt to the shared change reader. Invocation-wide Bash environment does not prove agent identity, so implicit selection remains unavailable in Bash. --workspace cannot select another workspace. Apply/revert require explicit IDs and execute only in the native Bash process; mread recovers retained output without repeating a mutation. Background capture receipts may appear only in saved Diff."
	journalText = "Use companion MCP journal_batch for a batch of plan/add/set/log/remove operations and journal_read for trees (p, depth, view, and agent when ancestry is proven). Paths are stable sibling ordinals. A batch is atomic; a rejected operation leaves the tree unchanged. Task states are pending, working, done, blocked or dropped; blocked/dropped require reason. Context nodes retain constraints, notes retain established facts, tasks retain actionable work. Update the owning node instead of duplicating or contradicting it.\n\nThe MCP handler joins native tool-use metadata to an exact authenticated hook receipt. Missing caller evidence is a rejection, not root authority. Child journals remain unmounted until native Agent results establish their parent; mounted views are read-only. Use view own for an unbound child. Completing a native turn prepares a work report but does not finish authored tasks. Do not add a journal-only finish call or rewrite the substantive answer. Record a plan with reset: slice to use journal-only context reset between completed slices. The shared UI continues runnable work after successful turns, with an Escape-cancellable countdown. Mark input-needed tasks blocked and completed tasks done. Reset uses a fresh native session and retained journal context, not a provider summary; it waits for background work and permissions. Do not record transient handles as resumable state. Classic native compaction with extra instructions still adds bounded recovery to the native summary."
	if err := os.MkdirAll(filepath.Join(plugin, ".claude-plugin"), 0700); err != nil {
		return result, registry, err
	}
	skillDir := filepath.Join(plugin, "skills", "mekugi")
	if err := os.MkdirAll(skillDir, 0700); err != nil {
		return result, registry, err
	}
	skill := strings.NewReplacer("{{UTILITIES}}", utilitiesText, "{{JOURNAL}}", journalText).Replace(claudeCompanionSkill)
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skill), 0600); err != nil {
		return result, registry, err
	}
	if err := os.WriteFile(filepath.Join(plugin, ".claude-plugin", "plugin.json"), []byte(`{"name":"mekugi","version":"1.0.0","description":"Invocation-local Mekugi companion"}`), 0600); err != nil {
		return result, registry, err
	}
	if registry != nil {
		var guide strings.Builder
		for _, entry := range registry.ordered {
			if !entry.Executable || len(entry.Specification) == 0 {
				continue
			}
			var spec struct {
				Description string `json:"description"`
			}
			if err := json.Unmarshal(entry.Specification, &spec); err != nil {
				return result, registry, err
			}
			guide.WriteString("## " + entry.Name + "\n\n" + spec.Description + "\n\n")
		}
		if err := os.WriteFile(filepath.Join(skillDir, "frontends.md"), []byte(guide.String()), 0600); err != nil {
			return result, registry, err
		}
	}
	hooks := filepath.Join(plugin, "hooks")
	if err := os.MkdirAll(hooks, 0700); err != nil {
		return result, registry, err
	}
	if err := os.WriteFile(filepath.Join(hooks, "hooks.json"), []byte(`{"modules":["./register.js"]}`), 0600); err != nil {
		return result, registry, err
	}
	// Native engine attachments are recomputed on resume, unlike pinned system
	// text. Keep the native attachment and custom-child policy intact.
	paths, err := json.Marshal(map[string]string{"skill": filepath.Join(skillDir, "SKILL.md"), "frontends": filepath.Join(skillDir, "frontends.md")}, jsontext.EscapeForJS(true))
	if err != nil {
		return result, registry, err
	}
	mod := `const paths = ` + string(paths) + `;
export function register(on) {
  on('prompt.attachment', {type: 'date'}, async ($, e, next) => {
    if (e.origin.kind !== 'engine') return next(e);
    const skill = await $.fs.read(paths.skill);
    const workflow = skill.replace(/^---\r?\n[\s\S]*?\r?\n---\r?\n/, '').trim();
    const frontends = paths.frontends;
    const contracts = await $.fs.read(frontends);
    const attachment = await next(e);
    return {text: (attachment.text ?? '') + '\n\n' + workflow + '\n\n' + contracts + '\n\nFrontend recovery: ' + frontends};
  });
}
`
	if err := os.WriteFile(filepath.Join(hooks, "register.js"), []byte(mod), 0600); err != nil {
		return result, registry, err
	}
	result.Plugin = plugin
	return result, registry, nil
}

func (s *ObservationService) closeCompanion() error {
	if s.journal != nil {
		s.journal.journals.detachNative(s.journal.sink())
	}
	var stagedErr error
	if s.stagedCompanion != nil {
		stagedErr = s.stagedCompanion.close()
	}
	return errors.Join(stagedErr, s.registry.Close(), os.RemoveAll(s.plugin))
}
