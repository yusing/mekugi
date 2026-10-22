package compact

import (
	json "encoding/json/v2"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/yusing/mekugi/libraries/commandargs"
)

// ReadCommands interprets selection metadata, never summary prose or file content.
func ReadCommands(selection string) ([]string, error) {
	if selection == "" {
		return nil, nil
	}
	selected, err := Sources(selection)
	if err != nil {
		return nil, err
	}

	var commands []string
	for _, source := range selected {
		path := source.Path
		if path == "-" {
			path = "./-"
		}
		args := []string{"cat", "--", path}
		if len(source.Ranges) > 0 {
			args = []string{"sed", "-n"}
		}
		valid := len(source.Path) <= 4096 && len(source.Skill) <= 256 && !strings.ContainsRune(source.Path, 0)
		if source.Path == "" {
			valid = valid && source.Skill != "" && !strings.ContainsAny(source.Skill, "\x00\r\n/") && !strings.HasPrefix(source.Skill, "-") && len(source.Ranges) == 0
			args = []string{"skills-mgr", "get", "--codex", source.Skill}
		} else if source.Skill != "" {
			valid = valid && filepath.Base(source.Path) == "SKILL.md"
		}
		for _, span := range source.Ranges {
			valid = valid && span[0] > 0 && span[1] >= span[0]
			args = append(args, "-e", fmt.Sprintf("%d,%dp", span[0], span[1]))
		}
		if source.Path != "" && len(source.Ranges) > 0 {
			args = append(args, "--", path)
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

// Source is selected context identity, independent of its command and outcome.
type Source struct {
	Path   string   `json:"path"`
	Skill  string   `json:"skill"`
	Ranges [][2]int `json:"ranges"`
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
