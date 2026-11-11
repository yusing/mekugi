package launch

import (
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yusing/mekugi/modules/conversation/completion"
)

// The picker owns adoption and cancellation; the runtime joins physical work,
// including helpers canceled by a retired or replaced picker.
type managedSkillLoad struct {
	parent    context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	retired   bool
	state     *completion.State
	revision  uint64
	target    completion.Target
	cwd       string
	choices   []completion.Choice
	available bool
	problem   string
}

func (p *composerPicker) loadManagedSkills(parent context.Context, workers *sync.WaitGroup, environment []string, target completion.Target, cwd string, choices []completion.Choice, available bool, problem string) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	load := &managedSkillLoad{parent: parent, cancel: cancel, done: make(chan struct{}), state: p.State, revision: p.skillsRevision, target: target, cwd: cwd}
	p.skillsLoad = load
	environment = slices.Clone(environment)
	workers.Go(func() {
		defer close(load.done)
		defer cancel()
		managed, err := readManagedSkills(ctx, cwd, environment)
		if err == nil {
			names := make(map[string]bool, len(managed))
			for _, choice := range managed {
				names[choice.Name] = true
			}
			choices = slices.DeleteFunc(choices, func(choice completion.Choice) bool { return names[choice.Name] })
			choices = append(choices, managed...)
			available = true
		} else {
			if problem != "" {
				problem += "; "
			}
			problem += "skills-mgr: " + err.Error()
		}
		load.choices, load.available, load.problem = choices, available, problem
	})
}

func (p *composerPicker) cancelManagedSkills() {
	if p.skillsLoad != nil {
		p.skillsLoad.retired = true
		p.skillsLoad.cancel()
	}
}

func (p *composerPicker) pollManagedSkills(attached *attachedPresentation, clock func() time.Time) bool {
	load := p.skillsLoad
	if load == nil {
		return false
	}
	select {
	case <-load.done:
	default:
		return false
	}
	p.skillsLoad = nil
	if load.retired || load.parent.Err() != nil || load.state != p.State || load.revision != p.skillsRevision || load.cwd != attached.cwd || p.Kind == "manage" {
		return true
	}
	p.finishSkillCatalog(attached, clock, load.choices, load.cwd, load.available, load.problem)
	p.Resolve(load.target, load.cwd, attached.cwd, load.choices, load.problem)
	return true
}

func (p *composerPicker) finishSkillCatalog(attached *attachedPresentation, clock func() time.Time, choices []completion.Choice, cwd string, available bool, problem string) {
	p.toggleOwnsManager = false
	p.Catalog(choices, cwd, available, problem)
	p.skillsRevision++
	if p.Kind == "manage" && p.Initial == nil {
		p.RememberSkills()
	}
	if problem != "" && cwd == attached.cwd {
		label := "Skill catalog incomplete: "
		if !available {
			label = "Skill attachment unavailable: "
		}
		setNotice(&attached.feedback, presentationTime(clock), label+problem+". Unavailable references remain text.", true)
	}
}

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

func readManagedSkills(ctx context.Context, cwd string, environment []string) ([]completion.Choice, error) {
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
	var choices []completion.Choice
	for _, skill := range document.Skills {
		if skill.Name != "" {
			choices = append(choices, completion.Choice{Name: skill.Name, Description: skill.Description, Enabled: true})
		}
	}
	return choices, nil
}
