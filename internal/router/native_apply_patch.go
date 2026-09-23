package router

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	sitter "github.com/tree-sitter/go-tree-sitter"
	"github.com/yusing/mekugi"
)

const (
	maxNativePatchFileBytes  = 8 << 20
	maxNativePatchTotalBytes = 24 << 20
)

type nativePatchObservation struct {
	Input string
	Files []nativePatchFileSnapshot
}

type nativePatchFileSnapshot struct {
	BeforePath   string `json:",omitempty"`
	AfterPath    string `json:",omitempty"`
	Before       string `json:",omitempty"`
	Exists       bool   `json:",omitzero"`
	Error        string `json:",omitempty"`
	TargetBefore string `json:",omitempty"`
	TargetExists bool   `json:",omitzero"`
	TargetError  string `json:",omitempty"`
}

type nativePatchPath struct {
	before string
	after  string
}

// nativePatchPaths extracts only the path ownership declared by a complete
// stock apply_patch envelope. The host remains the parser and executor; this
// observer never uses its result to authorize or reproduce an edit.
func nativePatchPaths(input, workspace string) ([]nativePatchPath, error) {
	lines := strings.Split(strings.TrimSuffix(input, "\n"), "\n")
	if len(lines) < 3 || strings.TrimSuffix(lines[0], "\r") != "*** Begin Patch" ||
		strings.TrimSuffix(lines[len(lines)-1], "\r") != "*** End Patch" {
		return nil, errors.New("incomplete apply_patch envelope")
	}
	resolve := func(path string) (string, error) {
		path = strings.TrimSpace(path)
		if path == "" || strings.ContainsRune(path, '\x00') {
			return "", errors.New("invalid apply_patch path")
		}
		if !filepath.IsAbs(path) {
			if !filepath.IsAbs(workspace) {
				return "", errors.New("relative apply_patch path requires a workspace")
			}
			path = filepath.Join(workspace, path)
		}
		return filepath.Clean(path), nil
	}
	var paths []nativePatchPath
	for index := 1; index < len(lines)-1; index++ {
		line := strings.TrimSuffix(lines[index], "\r")
		var before, after string
		switch {
		case strings.HasPrefix(line, "*** Add File: "):
			after = strings.TrimPrefix(line, "*** Add File: ")
		case strings.HasPrefix(line, "*** Delete File: "):
			before = strings.TrimPrefix(line, "*** Delete File: ")
		case strings.HasPrefix(line, "*** Update File: "):
			before = strings.TrimPrefix(line, "*** Update File: ")
			after = before
			if index+1 < len(lines)-1 {
				next := strings.TrimSuffix(lines[index+1], "\r")
				if moved, ok := strings.CutPrefix(next, "*** Move to: "); ok {
					after = moved
					index++
				}
			}
		default:
			continue
		}
		var err error
		if before != "" {
			before, err = resolve(before)
			if err != nil {
				return nil, err
			}
		}
		if after != "" {
			after, err = resolve(after)
			if err != nil {
				return nil, err
			}
		}
		paths = append(paths, nativePatchPath{before: before, after: after})
	}
	if len(paths) == 0 {
		return nil, errors.New("apply_patch envelope has no file operation")
	}
	return paths, nil
}

func readNativePatchFile(path string) (content string, exists bool, err error) {
	if path == "" {
		return "", false, nil
	}
	file, err := openNativePatchFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", false, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxNativePatchFileBytes {
		return "", true, errors.New("file is not a bounded regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxNativePatchFileBytes+1))
	if err != nil {
		return "", true, err
	}
	if len(data) > maxNativePatchFileBytes || !utf8.Valid(data) {
		return "", true, errors.New("file exceeds capture capacity or is not UTF-8")
	}
	return string(data), true, nil
}

func captureNativePatch(input, workspace string) (nativePatchObservation, error) {
	paths, err := nativePatchPaths(input, workspace)
	if err != nil {
		return nativePatchObservation{}, err
	}
	observation := nativePatchObservation{Input: input, Files: make([]nativePatchFileSnapshot, 0, len(paths))}
	total := len(input)
	for _, path := range paths {
		capturePath := path.before
		if capturePath == "" {
			capturePath = path.after
		}
		before, exists, captureErr := readNativePatchFile(capturePath)
		file := nativePatchFileSnapshot{BeforePath: path.before, AfterPath: path.after, Before: before, Exists: exists}
		if path.before == "" && exists {
			file.BeforePath = capturePath
		}
		if captureErr != nil {
			file.Error = captureErr.Error()
		}
		if path.before != "" && path.after != "" && path.before != path.after {
			file.TargetBefore, file.TargetExists, captureErr = readNativePatchFile(path.after)
			if captureErr != nil {
				file.TargetError = captureErr.Error()
			}
		}
		total += len(file.BeforePath) + len(file.AfterPath) + len(file.Before) + len(file.Error) + len(file.TargetBefore) + len(file.TargetError)
		if total > maxNativePatchTotalBytes {
			return nativePatchObservation{}, errors.New("apply_patch baseline exceeds capture capacity")
		}
		observation.Files = append(observation.Files, file)
	}
	return observation, nil
}

func nativePatchesInCall(name, input, workspace string) []nativePatchObservation {
	var patches []string
	if name == applyPatchToolName {
		patches = []string{input}
	} else if name == "exec" {
		patches = stockLiteralPatchInputs(input)
	}
	observations := make([]nativePatchObservation, 0, len(patches))
	total := 0
	for _, patch := range patches {
		observation, err := captureNativePatch(patch, workspace)
		if err == nil {
			for _, file := range observation.Files {
				total += len(file.Before) + len(file.BeforePath) + len(file.AfterPath) + len(file.Error) +
					len(file.TargetBefore) + len(file.TargetError)
			}
			if total > maxNativePatchTotalBytes {
				return nil
			}
			observations = append(observations, observation)
		}
	}
	return observations
}

// Inspect the entire Code Mode syntax tree rather than only transparent
// one-call wrappers. This recognizes literal patch arguments in ordinary JS
// sequencing and Promise batches, plus immutable top-level literal bindings
// used by a top-level expression. It never evaluates JavaScript or takes over
// the host tool. Dynamic arguments have no trustworthy pre-edit baseline.
func stockLiteralPatchInputs(source string) []string {
	if len(source) > maxMekugiScriptBytes {
		return nil
	}
	parser := sitter.NewParser()
	defer parser.Close()
	if parser.SetLanguage(codeModeJavaScriptLanguage) != nil {
		return nil
	}
	bytes := []byte(source)
	tree := parser.Parse(bytes, nil)
	if tree == nil {
		return nil
	}
	defer tree.Close()
	root := tree.RootNode()
	if root.HasError() {
		return nil
	}
	bindings := make(map[string]string)
	for index := range root.NamedChildCount() {
		statement := root.NamedChild(uint(index))
		if statement.Kind() != "lexical_declaration" || statement.ChildCount() == 0 ||
			statement.Child(0).Kind() != "const" {
			continue
		}
		for childIndex := range statement.NamedChildCount() {
			declaration := statement.NamedChild(uint(childIndex))
			if declaration.Kind() != "variable_declarator" {
				continue
			}
			name, value := declaration.ChildByFieldName("name"), declaration.ChildByFieldName("value")
			if name == nil || name.Kind() != "identifier" {
				continue
			}
			literal, ok := toolActivityStaticJavaScriptValue(value, bytes)
			if text, isText := literal.(string); ok && isText {
				bindings[name.Utf8Text(bytes)] = text
			}
		}
	}
	var patches []string
	for rootIndex := range root.NamedChildCount() {
		rootStatement := root.NamedChild(uint(rootIndex))
		allowBinding := rootStatement.Kind() == "expression_statement" ||
			rootStatement.Kind() == "lexical_declaration" || rootStatement.Kind() == "variable_declaration"
		type visit struct {
			node         *sitter.Node
			allowBinding bool
		}
		stack := []visit{{node: rootStatement, allowBinding: allowBinding}}
		for len(stack) != 0 {
			current := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			node := current.node
			if node != rootStatement {
				switch node.Kind() {
				case "arrow_function", "function_expression", "function_declaration",
					"generator_function", "generator_function_declaration", "method_definition", "class":
					current.allowBinding = false
				}
			}
			if args, ok := toolActivityCallArguments(node, bytes, "tools", applyPatchToolName); ok && len(args) == 1 {
				if value, ok := toolActivityStaticJavaScriptValue(args[0], bytes); ok {
					if patch, ok := value.(string); ok {
						patches = append(patches, patch)
					}
				} else if current.allowBinding && args[0].Kind() == "identifier" {
					if patch, ok := bindings[args[0].Utf8Text(bytes)]; ok {
						patches = append(patches, patch)
					}
				}
			}
			if len(patches) > 32 {
				return nil
			}
			for index := int(node.NamedChildCount()) - 1; index >= 0; index-- {
				stack = append(stack, visit{node: node.NamedChild(uint(index)), allowBinding: current.allowBinding})
			}
		}
	}
	return patches
}

// Detect a JavaScript string fragment containing a patch envelope even while
// the string or surrounding streamed program is incomplete. This is
// display-only suppression: evidence capture still requires a complete, valid
// program.
func stockPatchLiteralPresent(source string) bool {
	if len(source) > maxMekugiScriptBytes {
		return false
	}
	for offset := 0; offset < len(source); {
		if source[offset] != '\'' && source[offset] != '"' {
			offset++
			continue
		}
		value, consumed := toolActivityJavaScriptStringFragment(source[offset:])
		if strings.Contains(value, "*** Begin Patch") {
			return true
		}
		offset += max(1, consumed)
	}
	return false
}

// Decode arriving patch text without requiring the closing quote, call, or
// envelope. This is display-only and must never feed capture or execution.
func stockPatchFragment(source string) string {
	if len(source) > maxMekugiScriptBytes {
		return ""
	}
	ranges, _, _ := codeModePreviewSyntax(source)
	rangeIndex := 0
	patch := ""
	for at := 0; at < len(source); {
		for rangeIndex < len(ranges) && at >= ranges[rangeIndex].end {
			rangeIndex++
		}
		if rangeIndex < len(ranges) && at >= ranges[rangeIndex].start {
			at = ranges[rangeIndex].end
			continue
		}
		if next := codeModeSkipComment(source, at); next > at {
			at = next
			continue
		}
		if source[at] == '\'' || source[at] == '"' || source[at] == '`' {
			value, consumed := toolActivityJavaScriptStringFragment(source[at:])
			if _, ok := nativePatchPreview(value); ok {
				patch = value
			}
			at += max(1, consumed)
			continue
		}
		at++
	}
	return patch
}

func toolActivityJavaScriptStringFragment(source string) (string, int) {
	quote := source[0]
	var value strings.Builder
	for offset := 1; offset < len(source); {
		if quote == '`' && strings.HasPrefix(source[offset:], "${") {
			return value.String(), codeModeSkipString(source, 0)
		}
		if source[offset] == quote {
			return value.String(), offset + 1
		}
		if quote == '`' && (source[offset] == '\r' || source[offset] == '\n') {
			value.WriteByte(source[offset])
			offset++
			continue
		}
		if source[offset] == '\r' || source[offset] == '\n' {
			return value.String(), offset
		}
		if source[offset] == '\\' && offset+1 < len(source) && (strings.ContainsRune(`'"/\\`, rune(source[offset+1])) || quote == '`' && (source[offset+1] == '`' || source[offset+1] == '$')) {
			value.WriteByte(source[offset+1])
			offset += 2
			continue
		}
		char, _, tail, err := strconv.UnquoteChar(source[offset:], quote)
		if err != nil {
			// Go and JavaScript accept different escape sets. If this display-only
			// decoder cannot interpret one, recover at the string's lexical end so
			// a later patch literal is still inspected.
			for cursor := offset; cursor < len(source); {
				if source[cursor] == '\\' {
					cursor = min(len(source), cursor+2)
					continue
				}
				if source[cursor] == quote {
					return value.String(), cursor + 1
				}
				if source[cursor] == '\r' || source[cursor] == '\n' {
					return value.String(), cursor
				}
				_, size := utf8.DecodeRuneInString(source[cursor:])
				cursor += max(1, size)
			}
			return value.String(), len(source)
		}
		value.WriteRune(char)
		offset = len(source) - len(tail)
	}
	return value.String(), len(source)
}

func stockToolOutput(raw json.RawMessage) (text string, success *bool) {
	var direct string
	if json.Unmarshal(raw, &direct) == nil {
		return direct, nil
	}
	var object struct {
		Content json.RawMessage `json:"content"`
		Success *bool           `json:"success"`
	}
	if json.Unmarshal(raw, &object) == nil && (len(object.Content) != 0 || object.Success != nil) {
		texts := executionOutputTexts(object.Content)
		return strings.Join(texts, "\n"), object.Success
	}
	return strings.Join(executionOutputTexts(raw), "\n"), nil
}

// Code Mode's outer completion does not prove a nested apply_patch succeeded:
// JavaScript can catch a rejected tool call and finish normally. Nor does a
// yielded cell's first output finish the nested call. Only a terminal host
// result may be reconciled with the observed workspace outcome.
func stockPatchResultState(toolName string, raw json.RawMessage) (terminal, reportedSuccess bool, text, cell string) {
	text, success := stockToolOutput(raw)
	if toolName == applyPatchToolName {
		if success != nil {
			return true, *success, text, ""
		}
		return true, strings.HasPrefix(strings.TrimSpace(text), "Success."), text, ""
	}
	state, cell, _ := codeModeExecutionHeader(text)
	switch state {
	case "running":
		return false, false, text, cell
	case "Script completed":
		return true, true, text, ""
	case "Script failed", "Script terminated":
		return true, false, text, ""
	default:
		return false, false, text, ""
	}
}

func nativePatchReview(files []nativePatchFileSnapshot, remember func(string, string, bool)) ([]mekugi.ReviewFile, bool) {
	reviews := make([]mekugi.ReviewFile, 0, len(files))
	complete := true
	appendDifference := func(pathBefore, pathAfter, before, after string, existedBefore, existsAfter bool) {
		if existedBefore == existsAfter && before == after && (pathBefore == pathAfter || !existedBefore) {
			return
		}
		if !existedBefore {
			pathBefore = ""
		}
		if !existsAfter {
			pathAfter = ""
		}
		reviews = append(reviews, mekugi.RenderReviewFile(pathBefore, pathAfter, before, after))
	}
	for _, file := range files {
		readPath := file.AfterPath
		if readPath == "" {
			readPath = file.BeforePath
		}
		after, afterExists, afterErr := readNativePatchFile(readPath)
		move := file.BeforePath != "" && file.AfterPath != "" && file.BeforePath != file.AfterPath
		var sourceAfter string
		var sourceExists bool
		var sourceErr error
		if move {
			sourceAfter, sourceExists, sourceErr = readNativePatchFile(file.BeforePath)
		}
		if file.Error != "" || file.TargetError != "" || afterErr != nil || sourceErr != nil {
			reason := strings.Join([]string{file.Error, file.TargetError}, "; ")
			reason = strings.Trim(reason, "; ")
			for _, err := range []error{afterErr, sourceErr} {
				if err != nil {
					if reason != "" {
						reason += "; "
					}
					reason += err.Error()
				}
			}
			reviews = append(reviews, mekugi.RenderIncompleteReviewFile(file.BeforePath, file.AfterPath, reason))
			complete = false
			continue
		}
		if remember != nil {
			remember(readPath, after, afterExists)
			if move {
				remember(file.BeforePath, sourceAfter, sourceExists)
			}
		}
		if !move {
			appendDifference(file.BeforePath, readPath, file.Before, after, file.Exists, afterExists)
			continue
		}
		if file.Exists && !sourceExists && !file.TargetExists && afterExists {
			appendDifference(file.BeforePath, file.AfterPath, file.Before, after, true, true)
			continue
		}
		appendDifference(file.BeforePath, file.BeforePath, file.Before, sourceAfter, file.Exists, sourceExists)
		appendDifference(file.AfterPath, file.AfterPath, file.TargetBefore, after, file.TargetExists, afterExists)
	}
	return reviews, complete
}

func nativePatchDerivedCallID(callID string, index int) string {
	return callID + ":apply:" + strconv.Itoa(index+1)
}

func (p *mekugiProxy) finalizeNativePatches(ctx context.Context, workspace, thread, callID string, history mekugiHistory, output json.RawMessage) error {
	if len(history.NativePatches) == 0 {
		return nil
	}
	terminal, reportedSuccess, resultText, _ := stockPatchResultState(history.ToolName, output)
	if !terminal {
		return nil
	}
	for index, observation := range history.NativePatches {
		derivedCallID := nativePatchDerivedCallID(callID, index)
		if retained, found, err := p.replayStore.lookup(ctx, workspace, derivedCallID); err != nil {
			return err
		} else if found {
			if retained.CorrelationID != callID+"\x00"+strconv.Itoa(index) || retained.Script != observation.Input {
				return errors.New("retained stock apply_patch observation is inconsistent")
			}
			continue
		}
		correlation := callID + "\x00" + strconv.Itoa(index)
		changeID, err := p.replayStore.reserveChange(ctx, workspace, thread, correlation)
		if err != nil {
			return err
		}
		var after []execFileSnapshot
		reviews, complete := nativePatchReview(observation.Files, func(path, content string, exists bool) {
			file := execFileSnapshot{Path: path}
			if exists {
				info, err := os.Lstat(path)
				if err != nil || !info.Mode().IsRegular() {
					return
				}
				file.Kind, file.Content = execFileText, content
			}
			after = append(after, file)
		})
		// Only a direct stock result establishes nested patch success. A Code
		// Mode cell can complete after catching a failed or skipped patch; its
		// observed differences remain reviewable but unconfirmed.
		success := history.ToolName == applyPatchToolName && reportedSuccess && complete
		attempt := mekugiHistory{
			ToolName:         applyPatchToolName,
			Script:           observation.Input,
			Root:             workspace,
			ExecutingThread:  thread,
			ChangeID:         changeID,
			CorrelationID:    correlation,
			Attempt:          1,
			Applied:          success,
			AlreadySatisfied: success && len(reviews) == 0,
			ReviewFiles:      reviews,
			Report:           resultText,
			CarrierKind:      codeModeCarrierCustom,
			CarrierName:      applyPatchToolName,
			CarrierPayload:   observation.Input,
			ReplayCarrier:    true,
			UpstreamItem: map[string]json.RawMessage{
				"type":    mustMarshalJSON("custom_tool_call"),
				"name":    mustMarshalJSON(applyPatchToolName),
				"call_id": mustMarshalJSON(derivedCallID),
				"input":   mustMarshalJSON(observation.Input),
				"status":  mustMarshalJSON("completed"),
			},
		}
		if history.ToolName == applyPatchToolName && !reportedSuccess {
			attempt.TranslationError = strings.TrimSpace(resultText)
			if attempt.TranslationError == "" {
				attempt.TranslationError = "stock apply_patch failed"
			}
		}
		if !complete && attempt.Report != "" {
			attempt.Report += "\nMekugi could not capture complete file evidence."
		}
		if err := p.replayStore.put(context.WithoutCancel(ctx), workspace, map[string]mekugiHistory{derivedCallID: attempt}); err != nil {
			return err
		}
		namespace := workspace
		if p.replayStore != nil {
			namespace += "\x00" + p.replayStore.scoped(ctx).handleNamespace()
		}
		for _, file := range after {
			p.execLastSeen.put(namespace, changeID, file)
		}
		if success {
			_ = p.replayStore.publishEditReceipt(context.WithoutCancel(ctx), workspace, thread, derivedCallID, p.activity)
		}
	}
	return nil
}

func nativePatchPreview(input string) (liveDiffPreview, bool) {
	// Incomplete input is useful provisional display, never application evidence.
	if input != "*** Begin Patch" && !strings.HasPrefix(input, "*** Begin Patch\n") && !strings.HasPrefix(input, "*** Begin Patch\r\n") {
		return liveDiffPreview{}, false
	}
	return liveDiffPreview{Input: input, DiffText: true, Status: "STREAMING PREVIEW"}, true
}
