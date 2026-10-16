package guidance

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yusing/mekugi/libraries/commandargs"

	"github.com/yusing/mekugi/libraries/commandanalysis"
	"github.com/yusing/mekugi/libraries/commandanalysis/shellsyntax"
	"github.com/yusing/mekugi/libraries/outputbudget/tokenizer"
	"mvdan.cc/sh/v3/syntax"
)

// Source: internal/router/context_reset_guidance.go:37:551@57f4b4c6
const (
	MaxTokens      = 12000
	Header         = "Guidance loaded at context reset.\n"
	maxSourceBytes = 32 << 20
)

var errBudget = errors.New("exceeds the guidance budget")

// FirstReadOrder preserves precedence, not source eligibility.
type Guidance struct {
	Tool           string   `json:"tool"`
	Name           string   `json:"name"`
	Commands       []string `json:"commands"`
	Text           string   `json:"text"`
	FirstReadOrder []string `json:"first_read_order,omitempty"`
	Skills         []Skill  `json:"skills,omitempty"`
}

type Skill struct {
	Name string `json:"name"`
	Path string `json:"path,omitempty"`
}

func (s Skill) key() string {
	key := "skill:" + s.Name
	if s.Path != "" {
		key += "\x00" + s.Path
	}
	return key
}

type Instruction struct {
	Text   string
	Global bool
}

type source struct {
	path, tool, name  string
	skill             Skill
	tier, declaration int
}

// Classification uses the instruction's origin, not the location of its target.
func priority(path, home, workspace string, instructions []Instruction) (int, int, bool) {
	forms := []string{path}
	if rel, err := filepath.Rel(workspace, path); err == nil && filepath.IsLocal(rel) {
		forms = append(forms, rel)
	}
	if rel, err := filepath.Rel(home, path); home != "" && err == nil && filepath.IsLocal(rel) {
		forms = append(forms, "~/"+rel, "$HOME/"+rel, "${HOME}/"+rel)
	}
	tier, declaration, offset := 3, 0, 0
	for _, instruction := range instructions {
		variants := forms
		// Global documents may be declared by basename under "In ~/.codex".
		if instruction.Global && filepath.Dir(path) == filepath.Join(home, ".codex") && strings.Contains(instruction.Text, "~/.codex") {
			variants = append(slices.Clone(forms), filepath.Base(path))
		}
		for _, form := range variants {
			position := instructionPathPosition(instruction.Text, form)
			if position < 0 {
				continue
			}
			priority := 0
			if !instruction.Global {
				priority = 1
				if filepath.Dir(path) != workspace {
					priority = 2
				}
			}
			if priority < tier || priority == tier && offset+position < declaration {
				tier, declaration = priority, offset+position
			}
		}
		offset += len(instruction.Text)
	}
	return tier, declaration, tier < 3
}

func (s Service) Collect(ctx context.Context, input Input) *Guidance {
	workspace := input.Workspace
	if !filepath.IsAbs(workspace) {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	codec, err := tokenizer.New()
	if err != nil {
		return nil
	}
	home, _ := os.UserHomeDir()
	instructions := input.Instructions
	firstOrder := input.FirstReadOrder
	named := func(path string) bool { _, _, ok := priority(path, home, workspace, instructions); return ok }
	sources := make(map[string]source)
	var lastOrder []string
	addSkill := func(skill Skill, tool, name string) {
		key := skill.key()
		if _, exists := sources[key]; !exists && skill.Name != "" {
			sources[key] = source{path: key, tool: tool, name: name, skill: skill, tier: -1}
			lastOrder = append(lastOrder, key)
		}
	}
	for _, selected := range input.SelectedSkills {
		addSkill(selected.Skill, selected.Tool, selected.Name)
	}
	for _, observation := range input.CompletedReads {
		if !observation.Restored && (observation.ResponseRound < 1 || observation.ResponseRound > 5) {
			continue
		}
		kind, name := observation.Tool, observation.Name
		for _, skill := range observation.Skills {
			addSkill(skill, kind, name)
		}
		for _, command := range observation.Commands {
			if observation.Restored && !observation.HasSkillIdentity {
				// Older snapshots encode managed user selections only as commands.
				for _, argv := range commands(command, home) {
					if len(argv) == 4 && argv[0] == "skills-mgr" && argv[1] == "get" && argv[2] == "--codex" {
						addSkill(Skill{Name: argv[3]}, kind, name)
					}
				}
			}
			for _, path := range s.reads(command, home) {
				tier, declaration, ok := priority(path, home, workspace, instructions)
				if !ok {
					continue
				}
				if !observation.Restored && !slices.Contains(lastOrder, path) {
					lastOrder = append(lastOrder, path)
				}
				sources[path] = source{path: path, tool: kind, name: name, tier: tier, declaration: declaration}
			}
		}
	}
	if firstOrder == nil {
		firstOrder = slices.Clone(lastOrder)
	}
	order := make([]source, 0, len(sources))
	for _, source := range sources {
		order = append(order, source)
	}
	rank := func(paths []string, path string) int {
		if i := slices.Index(paths, path); i >= 0 {
			return i
		}
		return len(paths)
	}
	slices.SortFunc(order, func(a, b source) int {
		if n := cmp.Compare(a.tier, b.tier); n != 0 {
			return n
		}
		if n := cmp.Compare(rank(firstOrder, a.path), rank(firstOrder, b.path)); n != 0 {
			return n
		}
		if n := cmp.Compare(rank(lastOrder, a.path), rank(lastOrder, b.path)); n != 0 {
			return n
		}
		if n := cmp.Compare(a.declaration, b.declaration); n != 0 {
			return n
		}
		return strings.Compare(a.path, b.path)
	})
	guidance := &Guidance{FirstReadOrder: firstOrder}
	var text, notices strings.Builder
	text.WriteString(Header)
	hidden := 0
	for _, source := range order {
		command := "cat "
		if source.tier == 2 {
			command = "inspect_file "
		}
		read := command + commandargs.QuoteArgument(source.path)
		body, err := "", ctx.Err()
		if source.skill.Name != "" {
			var data []byte
			managed := source.skill.Path == ""
			if err == nil {
				data, managed, err = s.readSkill(ctx, workspace, source.skill.Name, source.skill.Path)
			}
			read = "skills-mgr get --codex " + commandargs.QuoteArgument(source.skill.Name)
			body = "\nSkill: " + source.skill.Name + "\n"
			if source.skill.Path != "" {
				body = fmt.Sprintf("\nSkill: %s from %q\n", source.skill.Name, source.skill.Path)
			}
			if !managed {
				read = "cat " + commandargs.QuoteArgument(source.skill.Path)
			}
			body += string(data)
		} else if err == nil {
			body, err = s.snapshot(ctx, source.path, named, source.tier == 2)
		}
		if err == nil {
			var tokens int
			tokens, err = codec.Count(text.String() + body)
			if tokens > MaxTokens-256 {
				err = errBudget
			}
		}
		if err != nil {
			label := "Unavailable: "
			if errors.Is(err, errBudget) {
				label = "Omitted by budget: "
			}
			note := label + read + ": " + err.Error() + "\n"
			if tokens, countErr := codec.Count(notices.String() + note); countErr == nil && tokens <= 224 {
				notices.WriteString(note)
			} else {
				hidden++
			}
			continue
		}
		if guidance.Name == "" {
			guidance.Tool, guidance.Name = source.tool, source.name
		}
		guidance.Commands = append(guidance.Commands, read)
		if source.skill.Name != "" {
			guidance.Skills = append(guidance.Skills, source.skill)
		}
		text.WriteString(body)
	}
	if len(guidance.Commands) == 0 && notices.Len() == 0 && hidden == 0 {
		return nil
	}
	text.WriteString(notices.String())
	if hidden > 0 {
		fmt.Fprintf(&text, "%d more sources omitted or unavailable\n", hidden)
	}
	guidance.Text = text.String()
	return guidance
}

// Only literal simple top-level reads qualify. Ranges select a source, not
// retained lines; whole-source snapshots need no range accumulation or merging.
func commands(command commandanalysis.Command, home string) [][]string {
	program, err := syntax.NewParser().Parse(strings.NewReader(command.Command), "")
	if err != nil {
		return nil
	}
	var commands [][]string
	for _, statement := range program.Stmts {
		call, ok := statement.Cmd.(*syntax.CallExpr)
		if !ok || len(call.Assigns) != 0 || len(statement.Redirs) != 0 || statement.Background {
			continue
		}
		var argv []string
		for _, arg := range call.Args {
			value, literal := literal(arg, home)
			if path, ok := strings.CutPrefix(arg.Lit(), "~/"); ok && home != "" && !strings.ContainsAny(path, "*?[") {
				value, literal = filepath.Join(home, path), true
			}
			if !literal {
				argv = nil
				break
			}
			argv = append(argv, value)
		}
		if len(argv) == 0 {
			continue
		}
		commands = append(commands, argv)
	}
	return commands
}

func (s Service) reads(command commandanalysis.Command, home string) []string {
	var sources []string
	for _, argv := range commands(command, home) {
		var paths []string
		switch argv[0] {
		case "cat":
			if !slices.ContainsFunc(argv[1:], func(arg string) bool { return strings.HasPrefix(arg, "-") }) {
				paths = argv[1:]
			}
		case "inspect_file":
			for _, arg := range argv[1:] {
				if !strings.HasPrefix(arg, "-") {
					paths = append(paths, arg)
				}
			}
		case "mcat":
			if specs, _, err := commandargs.ParseReadBundle(argv[1:]); err == nil {
				for _, spec := range specs {
					paths = append(paths, spec.Path)
				}
			}
		}
		for _, path := range paths {
			if !filepath.IsAbs(path) {
				path = filepath.Join(command.Workdir, path)
			}
			sources = append(sources, filepath.Clean(path))
		}
	}
	return sources
}

// Substitute only plain HOME references in a copy of the syntax tree. The
// shared literal reader still rejects every other dynamic shell construct.
func literal(word *syntax.Word, home string) (string, bool) {
	var substitute func([]syntax.WordPart, bool) ([]syntax.WordPart, bool)
	substitute = func(parts []syntax.WordPart, quoted bool) ([]syntax.WordPart, bool) {
		parts = slices.Clone(parts)
		for i, part := range parts {
			switch value := part.(type) {
			case *syntax.ParamExp:
				var expression strings.Builder
				if err := syntax.NewPrinter().Print(&expression, value); err != nil || home == "" ||
					(expression.String() != "$HOME" && expression.String() != "${HOME}") ||
					(!quoted && strings.ContainsAny(home, " \t\r\n*?[")) {
					return nil, false
				}
				parts[i] = &syntax.SglQuoted{Value: home}
			case *syntax.DblQuoted:
				copy := *value
				var ok bool
				copy.Parts, ok = substitute(value.Parts, true)
				if !ok {
					return nil, false
				}
				parts[i] = &copy
			}
		}
		return parts, true
	}
	copy := *word
	var ok bool
	copy.Parts, ok = substitute(word.Parts, false)
	if !ok {
		return "", false
	}
	return shellsyntax.Literal(&copy)
}

func (s Service) snapshot(ctx context.Context, path string, named func(string) bool, structural bool) (string, error) {
	header := "\nFile: " + path + "\n"
	if structural {
		header = "\nStructure: " + path + "\n"
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	for _, root := range []string{"/proc", "/sys", "/dev"} {
		if resolved == root || strings.HasPrefix(resolved, root+"/") {
			return "", errors.New("system files are not guidance")
		}
	}
	if !named(resolved) {
		return "", errors.New("resolved path is not instruction-named")
	}
	file, err := openSource(resolved)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("not a regular file")
	}
	if info.Size() > int64(maxSourceBytes) {
		return "", errors.New("source exceeds the replay read limit")
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(maxSourceBytes)+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxSourceBytes {
		return "", errors.New("source exceeds the replay read limit")
	}
	if !utf8.Valid(data) {
		return "", errors.New("not UTF-8")
	}
	if structural {
		outline, err := s.outline.InspectSource(ctx, path, data)
		return header + outline, err
	}
	return header + strings.TrimRight(string(data), "\n") + "\n", nil
}

func instructionPathPosition(text, form string) int {
	pathByte := func(c byte) bool {
		return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("._-/~$", c) >= 0
	}
	for at := 0; ; {
		index := strings.Index(text[at:], form)
		if index < 0 {
			return -1
		}
		start, end := at+index, at+index+len(form)
		if (start == 0 || !pathByte(text[start-1])) && (end == len(text) || !pathByte(text[end]) || text[end] == '.' && (end+1 == len(text) || !pathByte(text[end+1]))) {
			return start
		}
		at = start + 1
	}
}
