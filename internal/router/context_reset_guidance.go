package router

import (
	"cmp"
	"context"

	"github.com/yusing/mekugi/libraries/commandanalysis"

	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"

	"github.com/yusing/mekugi/modules/fileread/read"

	"github.com/yusing/mekugi/modules/sourceoutline/outline"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"

	jsonv1 "encoding/json"

	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yusing/mekugi/libraries/commandanalysis/shellsyntax"
	"github.com/yusing/mekugi/libraries/outputbudget/tokenizer"
	"mvdan.cc/sh/v3/syntax"
)

const (
	maxResetGuidanceTokens = 12000
	resetGuidanceCallID    = "call_mekugi_guidance_"
	resetGuidanceHeader    = "Guidance loaded at context reset.\n"
)

var errResetGuidanceBudget = errors.New("exceeds the guidance budget")

// FirstReadOrder preserves precedence, not source eligibility.
type resetGuidance struct {
	Tool           string               `json:"tool"`
	Name           string               `json:"name"`
	Commands       []string             `json:"commands"`
	Text           string               `json:"text"`
	FirstReadOrder []string             `json:"first_read_order,omitempty"`
	Skills         []resetGuidanceSkill `json:"skills,omitempty"`
}

type resetGuidanceSkill struct {
	Name string `json:"name"`
	Path string `json:"path,omitempty"`
}

func (s resetGuidanceSkill) key() string {
	key := "skill:" + s.Name
	if s.Path != "" {
		key += "\x00" + s.Path
	}
	return key
}

type resetGuidanceInstruction struct {
	text   string
	global bool
}

type resetGuidanceSource struct {
	path, tool, name  string
	skill             resetGuidanceSkill
	tier, declaration int
}

// Contiguous model items share a response, including parallel calls. User
// input and tool results separate rounds; restored recovery is not model work.
func resetGuidanceEarlyReads(items []map[string]jsonv1.RawMessage, recoveryTexts []string) map[string]bool {
	early := make(map[string]bool)
	round, inResponse := 0, false
	for i, item := range items {
		id := responses.StringField(item, "call_id")
		if strings.HasPrefix(id, resetGuidanceCallID) {
			early[id] = true
			continue
		}
		kind, role := responses.StringField(item, "type"), responses.StringField(item, "role")
		if role == "assistant" && slices.Contains(recoveryTexts, strings.TrimSuffix(messageContentText(item["content"]), "\n")) {
			continue
		}
		if role == "assistant" && i+1 < len(items) && strings.HasPrefix(responses.StringField(items[i+1], "call_id"), resetGuidanceCallID) {
			continue
		}
		model := role == "assistant" || kind == "reasoning" || kind == "function_call" || kind == "custom_tool_call"
		if model {
			if !inResponse {
				round++
				inResponse = true
			}
			if round <= 5 && id != "" {
				early[id] = true
			}
		} else if role == "user" || kind == "function_call_output" || kind == "custom_tool_call_output" {
			inResponse = false
		}
	}
	return early
}

func resetGuidanceUserSkills(items []map[string]jsonv1.RawMessage) []resetGuidanceSkill {
	var skills []resetGuidanceSkill
	add := func(name, path string) {
		skill := resetGuidanceSkill{Name: name, Path: path}
		if name != "" && !slices.Contains(skills, skill) {
			skills = append(skills, skill)
		}
	}
	for _, item := range items {
		if responses.StringField(item, "role") != "user" {
			continue
		}
		text := messageContentText(item["content"])
		if strings.HasPrefix(strings.TrimSpace(text), "# AGENTS.md instructions for ") {
			continue
		}
		var parts []struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(item["content"], &parts) != nil {
			parts = append(parts, struct {
				Text string `json:"text"`
			}{text})
		}
		for _, part := range parts {
			if name, path := selectedSkillSource(part.Text); name != "" {
				add(name, path)
				continue
			}
			if frames, ok := attachments.Decode(part.Text); ok {
				for _, frame := range frames {
					name, path, _ := attachments.ParseSkillFrame(frame)
					add(name, path)
				}
				continue
			}
		}
	}
	return skills
}

func resetGuidanceInstructions(items []map[string]jsonv1.RawMessage, base, workspace string) []resetGuidanceInstruction {
	instructions := []resetGuidanceInstruction{{base, true}}
	for _, item := range items {
		if kind := responses.StringField(item, "type"); kind != "message" && kind != "" {
			continue
		}
		role, text := responses.StringField(item, "role"), messageContentText(item["content"])
		switch {
		case role == "developer" || role == "system":
			instructions = append(instructions, resetGuidanceInstruction{text, true})
		case role == "user" && strings.HasPrefix(strings.TrimSpace(text), "# AGENTS.md instructions for "):
			if global, repo, ok := strings.Cut(text, "--- project-doc ---"); ok {
				instructions = append(instructions, resetGuidanceInstruction{global, true}, resetGuidanceInstruction{repo, false})
			} else {
				header, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
				owner := strings.TrimPrefix(header, "# AGENTS.md instructions for ")
				rel, err := filepath.Rel(workspace, owner)
				instructions = append(instructions, resetGuidanceInstruction{text, err != nil || !filepath.IsLocal(rel)})
			}
		}
	}
	return instructions
}

// Classification uses the instruction's origin, not the location of its target.
func resetGuidancePriority(path, home, workspace string, instructions []resetGuidanceInstruction) (int, int, bool) {
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
		if instruction.global && filepath.Dir(path) == filepath.Join(home, ".codex") && strings.Contains(instruction.text, "~/.codex") {
			variants = append(slices.Clone(forms), filepath.Base(path))
		}
		for _, form := range variants {
			position := instructionPathPosition(instruction.text, form)
			if position < 0 {
				continue
			}
			priority := 0
			if !instruction.global {
				priority = 1
				if filepath.Dir(path) != workspace {
					priority = 2
				}
			}
			if priority < tier || priority == tier && offset+position < declaration {
				tier, declaration = priority, offset+position
			}
		}
		offset += len(instruction.text)
	}
	return tier, declaration, tier < 3
}

func collectResetGuidance(ctx context.Context, items []map[string]jsonv1.RawMessage, baseInstructions, workspace string, firstOrder []string, retainedSkills map[string][]resetGuidanceSkill, recoveryTexts ...string) *resetGuidance {
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
	instructions := resetGuidanceInstructions(items, baseInstructions, workspace)
	named := func(path string) bool {
		_, _, ok := resetGuidancePriority(path, home, workspace, instructions)
		return ok
	}
	completed := make(map[string]bool)
	for _, item := range items {
		if kind := responses.StringField(item, "type"); kind == "function_call_output" || kind == "custom_tool_call_output" {
			completed[responses.StringField(item, "call_id")] = true
		}
	}
	sources := make(map[string]resetGuidanceSource)
	var lastOrder []string
	addSkill := func(skill resetGuidanceSkill, tool, name string) {
		key := skill.key()
		if _, exists := sources[key]; !exists && skill.Name != "" {
			sources[key] = resetGuidanceSource{path: key, tool: tool, name: name, skill: skill, tier: -1}
			lastOrder = append(lastOrder, key)
		}
	}
	for _, skill := range resetGuidanceUserSkills(items) {
		addSkill(skill, "function_call", nativeExecCommandToolName)
	}
	early := resetGuidanceEarlyReads(items, recoveryTexts)
	for _, item := range items {
		kind, name := responses.StringField(item, "type"), responses.StringField(item, "name")
		id := responses.StringField(item, "call_id")
		if !completed[id] || !early[id] {
			continue
		}
		for _, skill := range retainedSkills[id] {
			addSkill(skill, kind, name)
		}
		var commands []commandanalysis.Command
		switch {
		case kind == "custom_tool_call" && name == "exec":
			commands, _ = commandanalysis.LiteralExecCommands(responses.StringField(item, "input"), workspace, "")
		case kind == "function_call" && name == nativeExecCommandToolName:
			if command, ok := commandanalysis.ExecCommandArguments(responses.StringField(item, "arguments"), workspace, ""); ok {
				commands = []commandanalysis.Command{command}
			}
		}
		for _, command := range commands {
			_, hasSkillIdentity := retainedSkills[id]
			if strings.HasPrefix(id, resetGuidanceCallID) && !hasSkillIdentity {
				// Older snapshots encode managed user selections only as commands.
				for _, argv := range resetGuidanceCommands(command, home) {
					if len(argv) == 4 && argv[0] == "skills-mgr" && argv[1] == "get" && argv[2] == "--codex" {
						addSkill(resetGuidanceSkill{Name: argv[3]}, kind, name)
					}
				}
			}
			for _, path := range resetGuidanceReads(command, home) {
				tier, declaration, ok := resetGuidancePriority(path, home, workspace, instructions)
				if !ok {
					continue
				}
				if !strings.HasPrefix(responses.StringField(item, "call_id"), resetGuidanceCallID) && !slices.Contains(lastOrder, path) {
					lastOrder = append(lastOrder, path)
				}
				sources[path] = resetGuidanceSource{path: path, tool: kind, name: name, tier: tier, declaration: declaration}
			}
		}
	}
	if firstOrder == nil {
		firstOrder = slices.Clone(lastOrder)
	}
	order := make([]resetGuidanceSource, 0, len(sources))
	for _, source := range sources {
		order = append(order, source)
	}
	rank := func(paths []string, path string) int {
		if i := slices.Index(paths, path); i >= 0 {
			return i
		}
		return len(paths)
	}
	slices.SortFunc(order, func(a, b resetGuidanceSource) int {
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
	guidance := &resetGuidance{FirstReadOrder: firstOrder}
	var text, notices strings.Builder
	text.WriteString(resetGuidanceHeader)
	hidden := 0
	for _, source := range order {
		command := "cat "
		if source.tier == 2 {
			command = "inspect_file "
		}
		read := command + shellsyntax.QuoteArgument(source.path)
		body, err := "", ctx.Err()
		if source.skill.Name != "" {
			var data []byte
			managed := source.skill.Path == ""
			if err == nil {
				data, managed, err = readSkillAttachment(ctx, workspace, os.Environ(), composerSkill{name: source.skill.Name, path: source.skill.Path})
			}
			read = "skills-mgr get --codex " + shellsyntax.QuoteArgument(source.skill.Name)
			body = "\nSkill: " + source.skill.Name + "\n"
			if source.skill.Path != "" {
				body = fmt.Sprintf("\nSkill: %s from %q\n", source.skill.Name, source.skill.Path)
			}
			if !managed {
				read = "cat " + shellsyntax.QuoteArgument(source.skill.Path)
			}
			body += string(data)
		} else if err == nil {
			body, err = resetGuidanceSnapshot(ctx, source.path, named, source.tier == 2)
		}
		if err == nil {
			var tokens int
			tokens, err = codec.Count(text.String() + body)
			if tokens > maxResetGuidanceTokens-256 {
				err = errResetGuidanceBudget
			}
		}
		if err != nil {
			label := "Unavailable: "
			if errors.Is(err, errResetGuidanceBudget) {
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
func resetGuidanceCommands(command commandanalysis.Command, home string) [][]string {
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
			value, literal := resetGuidanceLiteral(arg, home)
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

func resetGuidanceReads(command commandanalysis.Command, home string) []string {
	var sources []string
	for _, argv := range resetGuidanceCommands(command, home) {
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
			if specs, _, err := read.Parse(argv[1:]); err == nil {
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
func resetGuidanceLiteral(word *syntax.Word, home string) (string, bool) {
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

func resetGuidanceSnapshot(ctx context.Context, path string, named func(string) bool, structural bool) (string, error) {
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
	file, err := openResetGuidanceFile(resolved)
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
	if info.Size() > int64(maxReplayRecordBytes) {
		return "", errors.New("source exceeds the replay read limit")
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(maxReplayRecordBytes)+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxReplayRecordBytes {
		return "", errors.New("source exceeds the replay read limit")
	}
	if !utf8.Valid(data) {
		return "", errors.New("not UTF-8")
	}
	if structural {
		outline, err := outline.InspectSource(ctx, path, data)
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

func messageContentText(content jsonv1.RawMessage) string {
	var text string
	if jsonv1.Unmarshal(content, &text) == nil {
		return text
	}
	var parts []struct {
		Text string `json:"text"`
	}
	_ = jsonv1.Unmarshal(content, &parts)
	var joined strings.Builder
	for _, part := range parts {
		joined.WriteString(part.Text)
		joined.WriteByte('\n')
	}
	return joined.String()
}
