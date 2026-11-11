package compact

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yusing/mekugi/libraries/commandargs"
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
)

// SelectionSchema describes the shared compaction and delegate source selection.
const SelectionSchema = `{"type":"object","properties":{"sources":{"type":"array","items":{"type":"string"}}},"required":["sources"],"additionalProperties":false}`

// Source is the public reference-string selection, with no object alternative.
type Source = string

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
		commands = append(commands, source.readCommand())
	}
	return commands, nil
}

func (source SourceIdentity) readCommand() string {
	path := source.readPath()
	if path != "" && !filepath.IsAbs(path) {
		path = "./" + path
	}
	args := []string{"mcat", "--", path}
	valid := source.Problem == "" && len(source.Path) <= 4096 && len(source.Skill) <= 256 && !strings.ContainsRune(source.Path, 0)
	if source.Changes != nil {
		valid = valid && len(source.Changes) > 0 && source.Path == "" && source.Skill == "" && len(source.Ranges) == 0
		args = append([]string{"mchanges"}, source.Changes...)
	} else if source.Path == "" {
		valid = valid && attachments.ValidSkillReference(source.Skill)
		args = []string{"skills-mgr", "get", "--codex", source.Skill}
	} else if source.Skill != "" {
		valid = valid && attachments.ValidSkillReference(source.Skill)
	}
	for _, span := range source.Ranges {
		valid = valid && span[0] > 0 && span[1] >= span[0]
		args = append(args, fmt.Sprintf("%d:%d", span[0], span[1]))
	}
	if !valid {
		problem := source.Problem
		if problem == "" {
			problem = fmt.Sprintf("invalid reload source %.256q", source.Path+source.Skill)
		}
		args = []string{"bash", "-c", "printf '%s\\n' " + commandargs.Quote("CONTENT NOT ATTACHED: "+problem) + "; exit 1"}
	}
	for i := range args {
		args[i] = commandargs.Quote(args[i])
	}
	return strings.Join(args, " ")
}

func (s SourceIdentity) readPath() string {
	return (attachments.Reference{Path: s.Path, Skill: s.Skill}).ReadPath()
}

// SourceIdentity is internal resolved identity, not the public selection wire format.
// Its object codec is retained solely for native identities and older records.
// Public sources are reference strings, decoded by Sources.
type SourceIdentity struct {
	Path    string   `json:"path,omitempty"`
	Skill   string   `json:"skill,omitempty"`
	Ranges  [][2]int `json:"ranges,omitempty"`
	Changes []string `json:"changes,omitzero"`
	Problem string   `json:"-"`
}

func sourceReference(text string) SourceIdentity {
	ref, err := attachments.ParseReference(text)
	if err != nil {
		return SourceIdentity{Problem: err.Error()}
	}
	return SourceIdentity{Path: ref.Path, Skill: ref.Skill, Ranges: ref.Ranges, Changes: ref.Changes}
}

func (s SourceIdentity) Reference() string {
	if s.Problem != "" || s.Changes != nil && (s.Path != "" || s.Skill != "" || len(s.Ranges) != 0) {
		return "invalid retained source identity"
	}
	return (attachments.Reference{Path: s.Path, Skill: s.Skill, Ranges: s.Ranges, Changes: s.Changes}).String()
}

// Selection encodes reference strings, never the internal source-object codec.
func Selection(sources []SourceIdentity) string {
	refs := make([]string, len(sources))
	for i, source := range sources {
		refs[i] = source.Reference()
	}
	data, _ := json.Marshal(struct {
		Sources []Source `json:"sources"`
	}{refs})
	return string(data)
}

// HostLoaded sources are supplied by Codex's instruction discovery.
func (s SourceIdentity) HostLoaded() bool {
	return s.Changes == nil && filepath.Base(s.readPath()) == "AGENTS.md"
}

// ReloadSources excludes host-owned instructions while Sources preserves the
// original order for interpreting already retained capture results.
func ReloadSources(selection string) ([]SourceIdentity, error) {
	sources, err := StoredSources(selection)
	return slices.DeleteFunc(sources, SourceIdentity.HostLoaded), err
}

func Sources(selection string) ([]SourceIdentity, error) {
	if selection == "" {
		return nil, nil
	}
	var selected struct {
		Sources []jsontext.Value `json:"sources"`
	}
	if json.Unmarshal([]byte(selection), &selected, json.RejectUnknownMembers(true)) != nil || selected.Sources == nil {
		return nil, errors.New("invalid compaction reload selection")
	}
	sources := make([]SourceIdentity, len(selected.Sources))
	for i, raw := range selected.Sources {
		var ref Source
		if raw.Kind() != '"' || json.Unmarshal(raw, &ref) != nil {
			return nil, errors.New("invalid compaction reload selection")
		}
		sources[i] = sourceReference(ref)
	}
	return sources, nil
}

// StoredSources reads already retained identities, including the historical
// object codec. New tool arguments and completion selections use Sources only.
func StoredSources(selection string) ([]SourceIdentity, error) {
	if sources, err := Sources(selection); err == nil {
		return sources, nil
	}
	var selected struct {
		Sources []SourceIdentity `json:"sources"`
	}
	if json.Unmarshal([]byte(selection), &selected, json.RejectUnknownMembers(true)) != nil || selected.Sources == nil {
		return nil, errors.New("invalid retained source selection")
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
