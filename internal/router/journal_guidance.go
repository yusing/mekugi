package router

import (
	"context"
	jsonv1 "encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"mvdan.cc/sh/v3/syntax"
)

const (
	maxJournalGuidanceBytes = 48 << 10
	journalGuidanceCallID   = "call_mekugi_guidance_"
	journalGuidanceHeader   = "Guidance loaded before context reset, read again at reset.\n"
	journalGuidanceNotice   = "Retained guidance: the next tool result holds guidance loaded before this reset.\n"
)

var errJournalGuidanceBudget = errors.New("exceeds the guidance budget")

// A retained call names whole sources, so later resets can snapshot them again.
type journalGuidance struct {
	Tool     string   `json:"tool"`
	Name     string   `json:"name"`
	Commands []string `json:"commands"`
	Text     string   `json:"text"`
}

// Sources are recognized and admitted newest first. Snapshot failures are
// advisory; both content and notices share one output budget.
func collectJournalGuidance(ctx context.Context, items []map[string]jsonv1.RawMessage, baseInstructions, workspace string) *journalGuidance {
	if !filepath.IsAbs(workspace) {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	home, _ := os.UserHomeDir()
	instructions := baseInstructions
	completed := make(map[string]bool)
	for _, item := range items {
		switch jsonString(item, "type") {
		case "function_call_output", "custom_tool_call_output":
			completed[jsonString(item, "call_id")] = true
		case "message", "":
			role, text := jsonString(item, "role"), journalMessageText(item["content"])
			if role == "developer" || role == "system" || role == "user" && strings.HasPrefix(strings.TrimSpace(text), "# AGENTS.md instructions for ") {
				instructions += "\n" + text
			}
		}
	}
	named := func(path string) bool {
		forms := []string{path}
		if rel, err := filepath.Rel(workspace, path); err == nil && filepath.IsLocal(rel) {
			forms = append(forms, rel)
		}
		if rel, err := filepath.Rel(home, path); home != "" && err == nil && filepath.IsLocal(rel) {
			forms = append(forms, "~/"+rel, "$HOME/"+rel)
		}
		for _, form := range forms {
			if journalNamesToken(instructions, form) {
				return true
			}
		}
		return false
	}
	guidance := &journalGuidance{}
	var text, notices strings.Builder
	text.WriteString(journalGuidanceHeader)
	seen := make(map[string]bool)
	hidden := 0
	for _, item := range slices.Backward(items) {
		kind, name := jsonString(item, "type"), jsonString(item, "name")
		if !completed[jsonString(item, "call_id")] {
			continue
		}
		var commands []execCommandInput
		switch {
		case kind == "custom_tool_call" && name == "exec": // Stock code-mode owner.
			commands, _ = stockLiteralExecCommands(jsonString(item, "input"), workspace, "")
		case kind == "function_call" && name == nativeExecCommandToolName:
			if command, ok := execCommandArguments(jsonString(item, "arguments"), workspace, ""); ok {
				commands = []execCommandInput{command}
			}
		}
		for _, command := range slices.Backward(commands) {
			for _, path := range slices.Backward(journalGuidanceReads(command, home)) {
				read := "cat " + shellQuoteArgument(path)
				if seen[read] || !named(path) {
					continue
				}
				seen[read] = true
				budget := maxJournalGuidanceBytes - text.Len() - 1024
				body, err := "", errJournalGuidanceBudget
				if budget > 0 {
					err = ctx.Err()
					if err == nil {
						body, err = journalGuidanceSnapshot(path, named, budget)
					}
				}
				if err != nil {
					label := "Unavailable: "
					if errors.Is(err, errJournalGuidanceBudget) {
						label = "Omitted by budget: "
					}
					note := label + read + ": " + err.Error() + "\n"
					if notices.Len()+len(note) <= 896 {
						notices.WriteString(note)
					} else {
						hidden++
					}
					continue
				}
				if guidance.Name == "" {
					guidance.Tool, guidance.Name = kind, name
				}
				guidance.Commands = append(guidance.Commands, read)
				text.WriteString(body)
			}
		}
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
func journalGuidanceReads(command execCommandInput, home string) []string {
	program, err := syntax.NewParser().Parse(strings.NewReader(command.Command), "")
	if err != nil {
		return nil
	}
	var sources []string
	for _, statement := range program.Stmts {
		call, ok := statement.Cmd.(*syntax.CallExpr)
		if !ok || len(call.Assigns) != 0 || len(statement.Redirs) != 0 || statement.Background {
			continue
		}
		var argv []string
		for _, arg := range call.Args {
			value, literal := shellCatLiteral(arg)
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
		var paths []string
		switch argv[0] {
		case "cat":
			if !slices.ContainsFunc(argv[1:], func(arg string) bool { return strings.HasPrefix(arg, "-") }) {
				paths = argv[1:]
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

func journalGuidanceSnapshot(path string, named func(string) bool, budget int) (string, error) {
	header := "\nFile: " + path + "\n"
	budget -= len(header) + 1
	if budget <= 0 {
		return "", errJournalGuidanceBudget
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
	if info.Size() > int64(budget) {
		return "", errJournalGuidanceBudget
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(budget)+1))
	if err != nil {
		return "", err
	}
	if len(data) > budget {
		return "", errJournalGuidanceBudget
	}
	if !utf8.Valid(data) {
		return "", errors.New("not UTF-8")
	}
	return header + strings.TrimRight(string(data), "\n") + "\n", nil
}

// journalNamesToken reports whether text names form as a whole path token.
func journalNamesToken(text, form string) bool {
	pathByte := func(c byte) bool {
		return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("._-/~$", c) >= 0
	}
	for at := 0; ; {
		index := strings.Index(text[at:], form)
		if index < 0 {
			return false
		}
		start, end := at+index, at+index+len(form)
		if (start == 0 || !pathByte(text[start-1])) && (end == len(text) || !pathByte(text[end]) || text[end] == '.' && (end+1 == len(text) || !pathByte(text[end+1]))) {
			return true
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
	if guidance.Name == "" || len(guidance.Commands) == 0 || !strings.HasPrefix(guidance.Text, journalGuidanceHeader) || len(guidance.Text) > maxJournalGuidanceBytes {
		return nil, errors.New("invalid retained journal guidance")
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
