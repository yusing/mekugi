package router

import (
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func managedSkillsExecutable(cwd string, environment []string) (string, error) {
	executable := ""
	for _, entry := range environment {
		if path, ok := strings.CutPrefix(entry, "PATH="); ok {
			for _, directory := range filepath.SplitList(path) {
				if !filepath.IsAbs(directory) {
					directory = filepath.Join(cwd, directory)
				}
				candidate := filepath.Join(directory, "skills-mgr")
				if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
					executable = candidate
					break
				}
			}
			break
		}
	}
	if executable == "" {
		return "", fmt.Errorf("executable not found in the wrapped PATH")
	}
	return executable, nil
}

func readManagedSkills(ctx context.Context, cwd string, environment []string) ([]composerChoice, error) {
	executable, err := managedSkillsExecutable(cwd, environment)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, executable, "list", "--codex")
	cmd.Dir, cmd.Env, cmd.WaitDelay = cwd, environment, time.Second
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var document struct {
		XMLName xml.Name `xml:"skills"`
		Skills  []struct {
			Name        string `xml:"name,attr"`
			Description string `xml:"description,attr"`
		} `xml:"skill"`
	}
	if err := xml.Unmarshal(output, &document); err != nil {
		return nil, fmt.Errorf("invalid catalog: %w", err)
	}
	var choices []composerChoice
	for _, skill := range document.Skills {
		if skill.Name != "" {
			choices = append(choices, composerChoice{name: skill.Name, description: skill.Description, enabled: true})
		}
	}
	return choices, nil
}
