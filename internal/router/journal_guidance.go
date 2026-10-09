package router

import (
	"cmp"
	"context"
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

	"github.com/yusing/mekugi/internal/router/toolplugin"
	"github.com/yusing/mekugi/internal/tokenizer"

	"mvdan.cc/sh/v3/syntax"
)

const (
	maxJournalGuidanceTokens = 12000
	journalGuidanceCallID    = "call_mekugi_guidance_"
	journalGuidanceHeader    = "Guidance loaded at context reset.\n"
	journalGuidanceNotice    = "Auto-loaded guidance: the next tool result holds guidance for this reset.\n"
)

var errJournalGuidanceBudget = errors.New("exceeds the guidance budget")

// FirstReadOrder preserves precedence, not source eligibility.
type journalGuidance struct {
	Tool           string                 `json:"tool"`
	Name           string                 `json:"name"`
	Commands       []string               `json:"commands"`
	Text           string                 `json:"text"`
	FirstReadOrder []string               `json:"first_read_order,omitempty"`
	Skills         []journalGuidanceSkill `json:"skills,omitempty"`
}

type journalGuidanceSkill struct {
	Name string `json:"name"`
	Path string `json:"path,omitempty"`
}

func (s journalGuidanceSkill) key() string {
	key := "skill:" + s.Name
	if s.Path != "" {
		key += "\x00" + s.Path
	}
	return key
}

type journalGuidanceInstruction struct {
	text   string
	global bool
}

type journalGuidanceSource struct {
	path, tool, name  string
	skill             journalGuidanceSkill
	tier, declaration int
}

// Contiguous model items share a response, including parallel calls. User
// input and tool results separate rounds; restored recovery is not model work.
func journalGuidanceEarlyReads(items []map[string]jsonv1.RawMessage, recoveryTexts []string) map[string]bool {
	early := make(map[string]bool)
	round, inResponse := 0, false
	for i, item := range items {
		id := jsonString(item, "call_id")
		if strings.HasPrefix(id, journalGuidanceCallID) {
			early[id] = true
			continue
		}
		kind, role := jsonString(item, "type"), jsonString(item, "role")
		if role == "assistant" && slices.Contains(recoveryTexts, strings.TrimSuffix(journalMessageText(item["content"]), "\n")) {
			continue
		}
		if role == "assistant" && i+1 < len(items) && strings.HasPrefix(jsonString(items[i+1], "call_id"), journalGuidanceCallID) {
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

func journalGuidanceUserSkills(items []map[string]jsonv1.RawMessage) []journalGuidanceSkill {
	var skills []journalGuidanceSkill
	add := func(name, path string) {
		skill := journalGuidanceSkill{Name: name, Path: path}
		if name != "" && !slices.Contains(skills, skill) {
			skills = append(skills, skill)
		}
	}
	for _, item := range items {
		if jsonString(item, "role") != "user" {
			continue
		}
		text := journalMessageText(item["content"])
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
			if frames, ok := decodeFileAttachments(part.Text); ok {
				for _, frame := range frames {
					name, path, _ := skillAttachmentFrame(frame)
					add(name, path)
				}
				continue
			}
		}
	}
	return skills
}

func journalGuidanceInstructions(items []map[string]jsonv1.RawMessage, base, workspace string) []journalGuidanceInstruction {
	instructions := []journalGuidanceInstruction{{base, true}}
	for _, item := range items {
		if kind := jsonString(item, "type"); kind != "message" && kind != "" {
			continue
		}
		role, text := jsonString(item, "role"), journalMessageText(item["content"])
		switch {
		case role == "developer" || role == "system":
			instructions = append(instructions, journalGuidanceInstruction{text, true})
		case role == "user" && strings.HasPrefix(strings.TrimSpace(text), "# AGENTS.md instructions for "):
			if global, repo, ok := strings.Cut(text, "--- project-doc ---"); ok {
				instructions = append(instructions, journalGuidanceInstruction{global, true}, journalGuidanceInstruction{repo, false})
			} else {
				header, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
				owner := strings.TrimPrefix(header, "# AGENTS.md instructions for ")
				rel, err := filepath.Rel(workspace, owner)
				instructions = append(instructions, journalGuidanceInstruction{text, err != nil || !filepath.IsLocal(rel)})
			}
		}
	}
	return instructions
}

// Classification uses the instruction's origin, not the location of its target.
func journalGuidancePriority(path, home, workspace string, instructions []journalGuidanceInstruction) (int, int, bool) {
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
			position := journalNamePosition(instruction.text, form)
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

func collectJournalGuidance(ctx context.Context, items []map[string]jsonv1.RawMessage, baseInstructions, workspace string, firstOrder []string, retainedSkills map[string][]journalGuidanceSkill, recoveryTexts ...string) *journalGuidance {
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
	instructions := journalGuidanceInstructions(items, baseInstructions, workspace)
	named := func(path string) bool {
		_, _, ok := journalGuidancePriority(path, home, workspace, instructions)
		return ok
	}
	completed := make(map[string]bool)
	for _, item := range items {
		if kind := jsonString(item, "type"); kind == "function_call_output" || kind == "custom_tool_call_output" {
			completed[jsonString(item, "call_id")] = true
		}
	}
	sources := make(map[string]journalGuidanceSource)
	var lastOrder []string
	addSkill := func(skill journalGuidanceSkill, tool, name string) {
		key := skill.key()
		if _, exists := sources[key]; !exists && skill.Name != "" {
			sources[key] = journalGuidanceSource{path: key, tool: tool, name: name, skill: skill, tier: -1}
			lastOrder = append(lastOrder, key)
		}
	}
	for _, skill := range journalGuidanceUserSkills(items) {
		addSkill(skill, "function_call", nativeExecCommandToolName)
	}
	early := journalGuidanceEarlyReads(items, recoveryTexts)
	for _, item := range items {
		kind, name := jsonString(item, "type"), jsonString(item, "name")
		id := jsonString(item, "call_id")
		if !completed[id] || !early[id] {
			continue
		}
		for _, skill := range retainedSkills[id] {
			addSkill(skill, kind, name)
		}
		var commands []execCommandInput
		switch {
		case kind == "custom_tool_call" && name == "exec":
			commands, _ = stockLiteralExecCommands(jsonString(item, "input"), workspace, "")
		case kind == "function_call" && name == nativeExecCommandToolName:
			if command, ok := execCommandArguments(jsonString(item, "arguments"), workspace, ""); ok {
				commands = []execCommandInput{command}
			}
		}
		for _, command := range commands {
			_, hasSkillIdentity := retainedSkills[id]
			if strings.HasPrefix(id, journalGuidanceCallID) && !hasSkillIdentity {
				// Older snapshots encode managed user selections only as commands.
				for _, argv := range journalGuidanceCommands(command, home) {
					if len(argv) == 4 && argv[0] == "skills-mgr" && argv[1] == "get" && argv[2] == "--codex" {
						addSkill(journalGuidanceSkill{Name: argv[3]}, kind, name)
					}
				}
			}
			for _, path := range journalGuidanceReads(command, home) {
				tier, declaration, ok := journalGuidancePriority(path, home, workspace, instructions)
				if !ok {
					continue
				}
				if !strings.HasPrefix(jsonString(item, "call_id"), journalGuidanceCallID) && !slices.Contains(lastOrder, path) {
					lastOrder = append(lastOrder, path)
				}
				sources[path] = journalGuidanceSource{path: path, tool: kind, name: name, tier: tier, declaration: declaration}
			}
		}
	}
	if firstOrder == nil {
		firstOrder = slices.Clone(lastOrder)
	}
	order := make([]journalGuidanceSource, 0, len(sources))
	for _, source := range sources {
		order = append(order, source)
	}
	rank := func(paths []string, path string) int {
		if i := slices.Index(paths, path); i >= 0 {
			return i
		}
		return len(paths)
	}
	slices.SortFunc(order, func(a, b journalGuidanceSource) int {
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
	guidance := &journalGuidance{FirstReadOrder: firstOrder}
	var text, notices strings.Builder
	text.WriteString(journalGuidanceHeader)
	hidden := 0
	for _, source := range order {
		command := "cat "
		if source.tier == 2 {
			command = "inspect_file "
		}
		read := command + shellQuoteArgument(source.path)
		body, err := "", ctx.Err()
		if source.skill.Name != "" {
			var data []byte
			managed := source.skill.Path == ""
			if err == nil {
				data, managed, err = readSkillAttachment(ctx, workspace, os.Environ(), composerSkill{name: source.skill.Name, path: source.skill.Path})
			}
			read = "skills-mgr get --codex " + shellQuoteArgument(source.skill.Name)
			body = "\nSkill: " + source.skill.Name + "\n"
			if source.skill.Path != "" {
				body = fmt.Sprintf("\nSkill: %s from %q\n", source.skill.Name, source.skill.Path)
			}
			if !managed {
				read = "cat " + shellQuoteArgument(source.skill.Path)
			}
			body += string(data)
			guidance.Tool, guidance.Name = source.tool, source.name
			guidance.Commands = append(guidance.Commands, read)
			guidance.Skills = append(guidance.Skills, source.skill)
		} else if err == nil {
			body, err = journalGuidanceSnapshot(ctx, source.path, named, source.tier == 2)
		}
		if err == nil {
			var tokens int
			tokens, err = codec.Count(text.String() + body)
			if tokens > maxJournalGuidanceTokens-256 {
				err = errJournalGuidanceBudget
			}
		}
		if err != nil {
			label := "Unavailable: "
			if errors.Is(err, errJournalGuidanceBudget) {
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
		if source.skill.Name == "" {
			guidance.Commands = append(guidance.Commands, read)
		}
		text.WriteString(body)
	}
	if len(guidance.Commands) == 0 {
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
func journalGuidanceCommands(command execCommandInput, home string) [][]string {
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
			value, literal := journalGuidanceLiteral(arg, home)
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

func journalGuidanceReads(command execCommandInput, home string) []string {
	var sources []string
	for _, argv := range journalGuidanceCommands(command, home) {
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
			if specs, _, err := parseReadBundle(argv[1:]); err == nil {
				for _, spec := range specs {
					paths = append(paths, spec.path)
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
func journalGuidanceLiteral(word *syntax.Word, home string) (string, bool) {
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
	return shellCatLiteral(&copy)
}

func journalGuidanceSnapshot(ctx context.Context, path string, named func(string) bool, structural bool) (string, error) {
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
	file, err := openJournalGuidanceFile(resolved)
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
		outline, err := toolplugin.InspectSource(ctx, path, data)
		return header + outline, err
	}
	return header + strings.TrimRight(string(data), "\n") + "\n", nil
}

// journalNamesToken reports whether text names form as a whole path token.
func journalNamesToken(text, form string) bool {
	return journalNamePosition(text, form) >= 0
}

func journalNamePosition(text, form string) int {
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

func journalMessageText(content jsonv1.RawMessage) string {
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

// journalGuidanceItems expands a retained snapshot into one completed call to
// the tool that loaded the newest source, preserving tool-result authority.
func journalGuidanceItems(guidance *journalGuidance, responseID string) ([]map[string]jsonv1.RawMessage, error) {
	if guidance == nil {
		return nil, nil
	}
	if guidance.Name == "" || len(guidance.Commands) == 0 ||
		!strings.HasPrefix(guidance.Text, journalGuidanceHeader) && !strings.HasPrefix(guidance.Text, "Guidance loaded before context reset, read again at reset.\n") {
		return nil, errors.New("invalid retained journal guidance")
	}
	codec, err := tokenizer.New()
	if err != nil {
		return nil, err
	}
	tokens, err := codec.Count(guidance.Text)
	// Older snapshots predate token-based admission. Preserve their exact
	// replay; the next reset snapshots their sources under the current budget.
	if err != nil || len(guidance.FirstReadOrder) > 0 && tokens > maxJournalGuidanceTokens {
		return nil, errors.New("invalid retained journal guidance token budget")
	}
	id := journalGuidanceCallID + strings.TrimPrefix(responseID, "resp_mekugi_compact_")
	switch guidance.Tool {
	case "custom_tool_call":
		var input strings.Builder
		for _, command := range guidance.Commands {
			fmt.Fprintf(&input, "text(await tools.%s({cmd:%s}));\n", nativeExecCommandToolName, mustMarshalJSON(command))
		}
		return []map[string]jsonv1.RawMessage{
			{"type": mustMarshalJSON("custom_tool_call"), "status": mustMarshalJSON("completed"), "call_id": mustMarshalJSON(id), "name": mustMarshalJSON(guidance.Name), "input": mustMarshalJSON(input.String())},
			{"type": mustMarshalJSON("custom_tool_call_output"), "call_id": mustMarshalJSON(id), "output": mustMarshalJSON([]any{map[string]any{"type": "input_text", "text": guidance.Text}})},
		}, nil
	case "function_call":
		arguments := mustMarshalJSON(map[string]string{"cmd": strings.Join(guidance.Commands, "; ")})
		return []map[string]jsonv1.RawMessage{
			{"type": mustMarshalJSON("function_call"), "call_id": mustMarshalJSON(id), "name": mustMarshalJSON(guidance.Name), "arguments": mustMarshalJSON(string(arguments))},
			{"type": mustMarshalJSON("function_call_output"), "call_id": mustMarshalJSON(id), "output": mustMarshalJSON(guidance.Text)},
		}, nil
	}
	return nil, errors.New("invalid retained journal guidance tool")
}
