package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/yusing/mekugi/capturer"
	codexinstructions "github.com/yusing/mekugi/contrib/codex"
)

const (
	mekugiInstructionsStartMarker = "<!-- mekugi-model-instructions:start -->"
	mekugiInstructionsEndMarker   = "<!-- mekugi-model-instructions:end -->"

	// Exact stock fragments make upstream prompt changes fail closed instead of
	// leaving conflicting editing guidance in the forwarded request.
	stockEditHeading            = "## File editing constraints"
	stockEditInstruction        = "Use `apply_patch` for local file edits. Do not create or edit files with `cat` or other shell write tricks. Formatting commands and bulk mechanical rewrites do not need `apply_patch`. Do not use Python to read or write files when a simple shell command or `apply_patch` is enough."
	stockRGInstruction          = "- When you search for text or files, you reach first for `rg` or `rg --files`; they are much faster than alternatives like `grep`. If `rg` is unavailable, you use the next best tool without fuss."
	stockExecInstruction        = "- Exercise caution when escaping text for exec_command calls - backticks and `$()` passed to the `cmd` argument will still execute. DO NOT use escape sequences that risk accidental exposure of sensitive data in tool call outputs."
	stockAstraIntroduction      = "You are Codex, an agent based on GPT-6. You and the user share one workspace, and your job is to collaborate with them until their intended goal is completely handled."
	stockWorkHeading            = "# Rules for getting work done"
	stockShellSafetyInstruction = "- Treat shell command text as code. `JSON.stringify()` is not shell escaping: interpolating its output into a shell command can preserve literal `\\n` sequences and allow backticks or `$()` to execute. Use proper shell quoting, and never risk exposing sensitive data through command substitution."
)

type instructionLine struct {
	number int
	start  int
	end    int
	text   string
}

func codexModelInstructionFileConfigured() (bool, error) {
	codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if codexHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return false, fmt.Errorf("determine Codex home: %w", err)
		}
		codexHome = filepath.Join(home, ".codex")
	}
	return modelInstructionFileConfiguredAt(filepath.Join(codexHome, "config.toml"))
}

func modelInstructionFileConfiguredAt(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read Codex config: %w", err)
	}
	var config struct {
		ModelInstructionFile *string `toml:"model_instructions_file"`
	}
	if err := toml.Unmarshal(data, &config); err != nil {
		return false, fmt.Errorf("parse Codex config: %w", err)
	}
	return config.ModelInstructionFile != nil, nil
}

func rewriteReceivedModelInstructions(ctx context.Context, request *parsedResponsesRequest, customized bool, modelInstructions string) (rewriteErr error) {
	evidence := capturer.InstructionRewrite{Carrier: "none", Strategy: "unchanged", Workflow: codexinstructions.WorkflowForModel(request.model()), CustomConfigured: customized}
	defer func() {
		if rewriteErr != nil {
			evidence.Strategy = "rejected"
		}
		capturer.ObserveInstructionRewrite(ctx, evidence)
	}()

	raw, present := request.fields["instructions"]
	var received *string
	if present {
		if err := json.Unmarshal(raw, &received); err != nil {
			evidence.Carrier, evidence.Strategy = "instructions", "rejected"
			return errors.New("responses instructions must be a string or null")
		}
	}
	if received == nil || *received == "" {
		rewritten, found, err := rewriteDeveloperModelInstructions(request.fields["input"], customized, modelInstructions, &evidence)
		if err != nil {
			return err
		}
		if found {
			request.setInput(rewritten)
			return rewriteDeveloperToolConflicts(request)
		}
	}
	if !present || received == nil {
		return rewriteDeveloperToolConflicts(request)
	}
	evidence.Carrier = "instructions"
	rendered, strategy, err := renderModelInstructions(*received, customized, modelInstructions)
	evidence.Strategy = strategy
	if err != nil {
		return err
	}
	request.fields["instructions"] = mustMarshalJSON(rendered)
	return rewriteDeveloperToolConflicts(request)
}

func requestUserInputIsPlanOnly(request *parsedResponsesRequest) bool {
	catalog := request.responseTools()
	if catalog.top == nil || catalog.top.err != nil {
		return false
	}
	for _, tool := range catalog.top.tools {
		if tool != nil && tool.Name == "request_user_input" &&
			strings.Contains(tool.Description, "This tool is only available in Plan mode.") {
			return true
		}
	}
	return false
}

func rewriteDeveloperToolConflicts(request *parsedResponsesRequest) error {
	raw := request.fields["input"]
	if len(raw) == 0 {
		return nil
	}
	input, err := decodeResponsesInput(raw)
	if err != nil {
		return fmt.Errorf("decode responses input instruction conflicts: %w", err)
	}
	if !input.array {
		return nil
	}
	rewriteConflicts := rewriteStockToolConflicts
	if requestUserInputIsPlanOnly(request) {
		rewriteConflicts = func(input string) string {
			return planOnlyDefaultModeConflictReplacer.Replace(rewriteStockToolConflicts(input))
		}
	}
	changed := false
	for index, rawItem := range input.items {
		item, ok := decodeResponsesItem(rawItem)
		if !ok || item.Type != "message" || item.Role != "developer" {
			continue
		}
		content, found, err := transformCTP2Content(item.Content, rewriteConflicts, isCTP2InputTextPart)
		if err != nil {
			return fmt.Errorf("rewrite developer instruction conflicts: %w", err)
		}
		if !found || string(content) == string(item.Content) {
			continue
		}
		item.setContent(content)
		input.items[index] = mustMarshalJSON(item)
		changed = true
	}
	if !changed {
		return nil
	}
	rewritten, err := input.encode()
	if err != nil {
		return fmt.Errorf("encode responses input instruction conflicts: %w", err)
	}
	request.setInput(rewritten)
	return nil
}

func rewriteDeveloperModelInstructions(raw json.RawMessage, customized bool, modelInstructions string, evidence *capturer.InstructionRewrite) (json.RawMessage, bool, error) {
	if len(raw) == 0 {
		return nil, false, nil
	}
	input, err := decodeResponsesInput(raw)
	if err != nil {
		return nil, false, fmt.Errorf("decode responses input instructions: %w", err)
	}
	var rewriteErr error
	found, err := transformFirstDeveloperText(&input, func(received string) string {
		evidence.Carrier = "developer"
		rendered, strategy, err := renderModelInstructions(received, customized, modelInstructions)
		evidence.Strategy = strategy
		if err != nil {
			rewriteErr = err
			return received
		}
		return rendered
	})
	if err != nil {
		return nil, false, fmt.Errorf("encode responses input instructions: %w", err)
	}
	if rewriteErr != nil {
		return nil, false, rewriteErr
	}
	if !found {
		return nil, false, nil
	}
	rewritten, err := input.encode()
	if err != nil {
		return nil, false, fmt.Errorf("encode responses input instructions: %w", err)
	}
	return rewritten, true, nil
}

func renderModelInstructions(input string, appendIfMissing bool, modelInstructions string) (string, string, error) {
	lines := instructionLines(input)
	starts := matchingInstructionLines(lines, mekugiInstructionsStartMarker)
	ends := matchingInstructionLines(lines, mekugiInstructionsEndMarker)
	if len(starts) != 0 || len(ends) != 0 {
		if len(starts) != 1 || len(ends) != 1 {
			return "", "rejected", errors.New("responses instructions contain incomplete mekugi markers")
		}
		if starts[0].number >= ends[0].number {
			return "", "rejected", errors.New("responses instructions contain reversed mekugi markers")
		}
		return rewriteStockToolConflicts(input[:starts[0].start]) + modelInstructions + rewriteStockToolConflicts(input[ends[0].end:]), "marked", nil
	}

	stockHeadings := matchingInstructionLines(lines, stockEditHeading)
	stockInstructions := matchingInstructionLines(lines, stockEditInstruction)
	stockRGInstructions := matchingInstructionLines(lines, stockRGInstruction)
	stockExecInstructions := matchingInstructionLines(lines, stockExecInstruction)
	if len(stockHeadings) == 1 && len(stockInstructions) == 1 && len(stockRGInstructions) == 1 && len(stockExecInstructions) == 1 {
		if stockInstructions[0].number == stockHeadings[0].number+2 && lines[stockHeadings[0].number].text == "" {
			return renderStockModelInstructions(lines, stockHeadings[0], stockInstructions[0], stockRGInstructions[0], stockExecInstructions[0], modelInstructions), "stock-gpt5", nil
		}
		if !appendIfMissing {
			return "", "rejected", errors.New("stock file-editing heading, separator, and instruction are not one section")
		}
	}

	// Astra has no editing section. Replace its pinned search line in the
	// work rules instead. The active template may already have replaced the
	// exec-command warning with the transport-independent shell safety rule.
	astraIntroductions := matchingInstructionLines(lines, stockAstraIntroduction)
	workHeadings := matchingInstructionLines(lines, stockWorkHeading)
	shellSafetyInstructions := matchingInstructionLines(lines, stockShellSafetyInstruction)
	execAnchor := instructionLine{}
	if len(stockExecInstructions) == 1 {
		execAnchor = stockExecInstructions[0]
	} else if len(stockExecInstructions) == 0 && len(shellSafetyInstructions) == 1 {
		execAnchor = shellSafetyInstructions[0]
	}
	if len(astraIntroductions) == 1 && len(workHeadings) == 1 &&
		len(stockHeadings) == 0 && len(stockInstructions) == 0 &&
		len(stockRGInstructions) == 1 && execAnchor.number != 0 &&
		astraIntroductions[0].number < workHeadings[0].number &&
		stockRGInstructions[0].number == workHeadings[0].number+2 &&
		lines[workHeadings[0].number].text == "" &&
		execAnchor.number > stockRGInstructions[0].number {
		displacedExec := instructionLine{}
		if len(stockExecInstructions) == 1 {
			displacedExec = stockExecInstructions[0]
		}
		return renderStockModelInstructions(lines, stockRGInstructions[0], stockRGInstructions[0], stockRGInstructions[0], displacedExec, modelInstructions), "stock-astra", nil
	}

	if appendIfMissing {
		if input == "" {
			return modelInstructions, "custom-append", nil
		}
		separator := "\n\n"
		if strings.HasSuffix(input, "\n") {
			separator = "\n"
		}
		return rewriteStockToolConflicts(input) + separator + modelInstructions, "custom-append", nil
	}
	return "", "rejected", errors.New("responses instructions match neither stock nor marked mekugi guidance")
}

func renderStockModelInstructions(lines []instructionLine, first, last, rgInstruction, execInstruction instructionLine, modelInstructions string) string {
	var rendered strings.Builder
	for _, line := range lines {
		if line.number == first.number {
			rendered.WriteString(modelInstructions)
		}
		if line.number >= first.number && line.number <= last.number ||
			line.number == rgInstruction.number || line.number == execInstruction.number {
			continue
		}
		rendered.WriteString(line.text)
		if line.end > line.start+len(line.text) {
			rendered.WriteByte('\n')
		}
	}
	return rewriteStockToolConflicts(rendered.String())
}

func instructionLines(input string) []instructionLine {
	if input == "" {
		return nil
	}
	lines := make([]instructionLine, 0, strings.Count(input, "\n")+1)
	for start, number := 0, 1; start < len(input); number++ {
		end := strings.IndexByte(input[start:], '\n')
		if end < 0 {
			end = len(input)
		} else {
			end += start + 1
		}
		textEnd := end
		if input[end-1] == '\n' {
			textEnd--
		}
		lines = append(lines, instructionLine{number: number, start: start, end: end, text: input[start:textEnd]})
		start = end
	}
	return lines
}

func matchingInstructionLines(lines []instructionLine, text string) []instructionLine {
	matches := make([]instructionLine, 0, 1)
	for _, line := range lines {
		if line.text == text {
			matches = append(matches, line)
		}
	}
	return matches
}
