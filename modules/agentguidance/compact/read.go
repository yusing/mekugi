package compact

import (
	json "encoding/json/v2"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yusing/mekugi/libraries/commandargs"
)

// SelectionSchema describes the shared compaction and delegate source selection.
// ReadCommands enforces exclusive changes sources; Claude's worker cannot compile `not`.
const SelectionSchema = `{"type":"object","properties":{"sources":{"type":"array","items":{"type":"object","properties":{"path":{"type":"string"},"skill":{"type":"string"},"changes":{"type":"array","items":{"type":"string"},"minItems":1},"ranges":{"type":"array","items":{"type":"array","items":{"type":"integer"},"minItems":2,"maxItems":2}}},"additionalProperties":false}}},"required":["sources"],"additionalProperties":false}`

// ReadCommands interprets selection metadata, never summary prose or file content.
func ReadCommands(selection string) ([]string, error) {
	if selection == "" {
		return nil, nil
	}
	selected, err := ReloadSources(selection)
	if err != nil {
		return nil, err
	}

	var commands []string
	for _, source := range selected {
		path := source.readPath()
		if path != "" && !filepath.IsAbs(path) {
			path = "./" + path
		}
		args := []string{"mcat", "--", path}
		valid := len(source.Path) <= 4096 && len(source.Skill) <= 256 && !strings.ContainsRune(source.Path, 0)
		if source.Changes != nil {
			valid = len(source.Changes) > 0 && source.Path == "" && source.Skill == "" && len(source.Ranges) == 0
			args = append([]string{"mchanges"}, source.Changes...)
		} else if source.Path == "" {
			valid = valid && validSkillReference(source.Skill)
			args = []string{"skills-mgr", "get", "--codex", source.Skill}
		} else if source.Skill != "" {
			valid = valid && validSkillReference(source.Skill)
		}
		for _, span := range source.Ranges {
			valid = valid && span[0] > 0 && span[1] >= span[0]
			args = append(args, fmt.Sprintf("%d:%d", span[0], span[1]))
		}
		if !valid {
			args = []string{"bash", "-c", "printf '%s\\n' " + commandargs.Quote(fmt.Sprintf("CONTENT NOT ATTACHED: invalid reload source %.256q", source.Path+source.Skill)) + "; exit 1"}
		}
		for i := range args {
			args[i] = commandargs.Quote(args[i])
		}
		commands = append(commands, strings.Join(args, " "))
	}
	return commands, nil
}

func (s Source) readPath() string {
	if _, reference, ok := strings.Cut(s.Skill, "/"); ok && filepath.Base(s.Path) == "SKILL.md" {
		return filepath.Join(filepath.Dir(s.Path), reference)
	}
	return s.Path
}

func validSkillReference(skill string) bool {
	if skill == "" || strings.HasPrefix(skill, "-") || strings.ContainsAny(skill, "\x00\r\n\\") {
		return false
	}
	for part := range strings.SplitSeq(skill, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// Source is selected context identity, independent of its command and outcome.
type Source struct {
	Path    string   `json:"path,omitempty"`
	Skill   string   `json:"skill,omitempty"`
	Ranges  [][2]int `json:"ranges,omitempty"`
	Changes []string `json:"changes,omitzero"`
}

// HostLoaded sources are supplied by Codex's instruction discovery.
func (s Source) HostLoaded() bool {
	return s.Changes == nil && filepath.Base(s.readPath()) == "AGENTS.md"
}

// ReloadSources excludes host-owned instructions while Sources preserves the
// original order for interpreting already retained capture results.
func ReloadSources(selection string) ([]Source, error) {
	sources, err := Sources(selection)
	return slices.DeleteFunc(sources, Source.HostLoaded), err
}

func Sources(selection string) ([]Source, error) {
	if selection == "" {
		return nil, nil
	}
	var selected struct {
		Sources []Source `json:"sources"`
	}
	if json.Unmarshal([]byte(selection), &selected, json.RejectUnknownMembers(true)) != nil || selected.Sources == nil {
		return nil, errors.New("invalid compaction reload selection")
	}
	return selected.Sources, nil
}

// CompletionSources keeps completion attachments limited to retained changes.
func CompletionSources(selection string) error {
	sources, err := Sources(selection)
	if err != nil {
		return err
	}
	for _, source := range sources {
		if len(source.Changes) == 0 || source.Path != "" || source.Skill != "" || len(source.Ranges) != 0 {
			return errors.New("completion reload requires explicit captured-change IDs/ranges")
		}
	}
	return nil
}
