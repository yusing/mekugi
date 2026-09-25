package router

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yusing/mekugi/internal/router/toolplugin"
	"mvdan.cc/sh/v3/syntax"
)

// Credential-gated: exploration output from stock exec_command is judged
// against the task by a decision model, and unrelated result groups are
// replaced with a summary whose full text stays readable through mread.
const (
	exploreMinBytes       = 1024
	exploreMinSavedBytes  = 512
	exploreMinSavedShare  = 0.25
	exploreMinUnits       = 4
	exploreMaxFileUnits   = 128
	exploreMaxUnits       = 384
	exploreBatchUnits     = 16
	exploreBatchChars     = 6000
	exploreConcurrency    = 16
	exploreSampleLines    = 6
	exploreDiffSample     = 12
	exploreSampleChars    = 160
	exploreLabelChars     = 72
	exploreKeepMin        = 0.25
	exploreKeepTop        = 3
	exploreListedBytes    = 600
	exploreJudgeTimeout   = 10 * time.Second
	exploreTaskChars      = 2000
	exploreIntentChars    = 1500
	exploreDecisionsLimit = 4096
)

// exploreFamily names how a command's rows split into independently
// judgeable units.
type exploreFamily int

const (
	exploreNone exploreFamily = iota
	explorePaths
	exploreDiagnostics
	exploreCommits
	exploreDiffs
	exploreHelp
)

var explorePathPrograms = map[string]bool{"rg": true, "grep": true, "egrep": true, "fgrep": true, "find": true, "fd": true, "fdfind": true}

// Diagnostic tools print one `path:line:col: message` row per finding; the
// value is the subcommand that selects that output, or "" for any.
var exploreDiagnosticPrograms = map[string]string{"staticcheck": "", "golangci-lint": "run", "gopls": "check", "tsc": "", "ruff": "check", "mypy": "", "flake8": "", "pylint": ""}

// Formats whose rows do not start with a path, or that span several rows per
// path, are not grouped.
var exploreUnsupportedFlags = map[string]bool{"--json": true, "--heading": true, "-p": true, "--pretty": true, "-0": true, "--null": true, "--print0": true, "-print0": true, "-ls": true, "-printf": true, "-fprintf": true, "-exec": true, "-execdir": true, "-ok": true, "-delete": true, "-x": true, "--exec": true, "-X": true, "--exec-batch": true, "-z": true, "-O": true, "--open-files-in-pager": true, "--graph": true}

var (
	exploreContextRow = regexp.MustCompile(`^(.+?)-[0-9]+-`)
	exploreParenRow   = regexp.MustCompile(`^(.+?)\([0-9]+,[0-9]+\):`)
	exploreCommitRow  = regexp.MustCompile(`^commit [0-9a-f]{7,64}\b`)
	exploreOnelineRow = regexp.MustCompile(`^[0-9a-f]{7,64} `)
	exploreOptionRow  = regexp.MustCompile(`^\s{2,}-`)
)

type exploreJudge interface {
	nouls(ctx context.Context, state any, questions map[string]typesafeNoul) (map[string]float64, typesafeUsage, error)
}

type exploreFilter struct {
	judge exploreJudge

	mu        sync.Mutex
	decisions map[string]*exploreDecision
	order     []string
}

// A decision is fixed on first evaluation: later requests replaying the same
// output must forward identical bytes, or the provider prefix would change.
type exploreDecision struct {
	done   chan struct{}
	output json.RawMessage // nil keeps the stock output
}

type exploreTask struct {
	UserRequest string `json:"user_request,omitempty"`
	AgentIntent string `json:"agent_intent,omitempty"`
	Command     string `json:"command"`
	Workdir     string `json:"workdir"`
}

// exploreUnitKind selects how a unit is described to the judge and in the
// omission summary.
type exploreUnitKind int

const (
	exploreFileUnit exploreUnitKind = iota
	exploreDirectoryUnit
	exploreCommitUnit
	exploreDiffUnit
	exploreHelpUnit
)

type exploreUnit struct {
	key   string
	rows  []int
	lines []string // sample source: row text after a path key, or whole rows
}

type exploreUnitState struct {
	Path       string   `json:"path,omitempty"`
	Directory  string   `json:"directory,omitempty"`
	Entries    int      `json:"entries,omitempty"`
	MatchCount int      `json:"match_count,omitempty"`
	Label      string   `json:"label,omitempty"`
	LineCount  int      `json:"line_count,omitempty"`
	Sample     []string `json:"sample,omitempty"`
}

func newExploreFilter(judge exploreJudge) *exploreFilter {
	return &exploreFilter{judge: judge, decisions: make(map[string]*exploreDecision)}
}

// project runs after replay validation. Only outputs new in this request are
// judged; older outputs reuse a recorded decision or stay unchanged.
func (f *exploreFilter) project(ctx context.Context, request *parsedResponsesRequest, visible map[string]mekugiHistory, directory, sessionShell, recipient string, store *mekugiReplayStore) {
	if f == nil || f.judge == nil {
		return
	}
	var items []map[string]json.RawMessage
	if json.Unmarshal(request.fields["input"], &items) != nil {
		return
	}
	trailing := len(items)
	for trailing > 0 && strings.HasSuffix(jsonString(items[trailing-1], "type"), "_output") {
		trailing--
	}
	userRequest := truncateExploreText(journalQuestionFromInput(request.fields["input"], recipient), exploreTaskChars)
	calls := make(map[string]int)
	type pendingJudgment struct {
		index    int
		decision *exploreDecision
	}
	var pending []pendingJudgment
	var wg sync.WaitGroup
	for index, item := range items {
		callID := jsonString(item, "call_id")
		switch jsonString(item, "type") {
		case "function_call", "custom_tool_call":
			if callID != "" {
				calls[callID] = index
			}
			continue
		case "function_call_output", "custom_tool_call_output":
		default:
			continue
		}
		call, found := map[string]json.RawMessage(nil), false
		if at, ok := calls[callID]; ok && callID != "" {
			call, found = items[at], true
		} else if history, ok := visible[callID]; ok && callID != "" {
			call, found = history.UpstreamItem, history.UpstreamItem != nil
		}
		if !found ||
			jsonString(call, "namespace") != "" && jsonString(call, "namespace") != "functions" {
			continue
		}
		name := strings.TrimPrefix(jsonString(call, "name"), "functions.")
		codeMode := name == "exec" && jsonString(call, "type") == "custom_tool_call" && jsonString(item, "type") == "custom_tool_call_output"
		arguments := jsonString(call, "arguments")
		if codeMode {
			source := jsonString(call, "input")
			if len(source) > maxMekugiScriptBytes {
				continue
			}
			nested, ok := toolActivityUnwrapExec(source, true)
			if !ok || jsonString(nested, "name") != nativeExecCommandToolName {
				continue
			}
			arguments = jsonString(nested, "arguments")
		} else if name != nativeExecCommandToolName || jsonString(item, "type") != "function_call_output" {
			continue
		}
		output := item["output"]
		digest := sha256.Sum256(append([]byte(callID+"\x00"), output...))
		key := string(digest[:])
		decision, owner := f.decision(key, index >= trailing)
		if decision == nil {
			continue
		}
		pending = append(pending, pendingJudgment{index: index, decision: decision})
		if !owner {
			continue
		}
		input, ok := execCommandArguments(arguments, directory, sessionShell)
		if !ok {
			close(decision.done)
			continue
		}
		task := exploreTask{UserRequest: userRequest, AgentIntent: exploreIntent(items, calls[callID]), Command: input.Command, Workdir: input.Workdir}
		wg.Go(func() {
			defer close(decision.done)
			if codeMode {
				decision.output = f.filterCodeMode(ctx, task, output, store)
				return
			}
			text, ok := decodeJSONString(output)
			if !ok {
				return
			}
			filtered, ok := f.filter(ctx, task, text, store)
			if ok {
				decision.output = mustMarshalJSON(filtered)
			}
		})
	}
	wg.Wait()
	changed := false
	for _, judgment := range pending {
		<-judgment.decision.done
		if judgment.decision.output != nil {
			items[judgment.index]["output"] = judgment.decision.output
			changed = true
		}
	}
	if changed {
		request.setInput(mustMarshalJSON(items))
	}
}

// decision returns a recorded decision, or registers a new one when create is
// set. The caller that registers it owns closing done.
func (f *exploreFilter) decision(key string, create bool) (*exploreDecision, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if decision, ok := f.decisions[key]; ok {
		return decision, false
	}
	if !create {
		return nil, false
	}
	decision := &exploreDecision{done: make(chan struct{})}
	f.decisions[key] = decision
	f.order = append(f.order, key)
	if len(f.order) > exploreDecisionsLimit {
		delete(f.decisions, f.order[0])
		f.order = f.order[1:]
	}
	return decision, true
}

// exploreIntent collects assistant text and readable reasoning summaries from
// the response that issued the call.
func exploreIntent(items []map[string]json.RawMessage, callIndex int) string {
	var parts []string
	for index := callIndex - 1; index >= 0; index-- {
		item := items[index]
		kind := jsonString(item, "type")
		if kind == "function_call" || kind == "custom_tool_call" {
			continue
		}
		if kind == "reasoning" {
			var summary []struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(item["summary"], &summary)
			for _, part := range slices.Backward(summary) {
				parts = append(parts, part.Text)
			}
			continue
		}
		if kind == "message" && jsonString(item, "role") == "assistant" {
			var content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			_ = json.Unmarshal(item["content"], &content)
			for _, part := range slices.Backward(content) {
				if part.Type == "output_text" {
					parts = append(parts, part.Text)
				}
			}
			continue
		}
		break
	}
	slices.Reverse(parts)
	text := strings.TrimSpace(strings.Join(parts, "\n"))
	if len(text) > exploreIntentChars {
		// The latest words sit closest to the call.
		text = strings.ToValidUTF8(text[len(text)-exploreIntentChars:], "")
	}
	return text
}

func truncateExploreText(text string, limit int) string {
	text = strings.TrimSpace(text)
	if len(text) <= limit {
		return text
	}
	return strings.ToValidUTF8(text[:limit], "")
}

// exploreCommand accepts one literal invocation of a program whose output
// splits into units. Pipes, lists, substitutions, and redirections change what
// the output means.
func exploreCommand(command string) exploreFamily {
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil || len(file.Stmts) != 1 {
		return exploreNone
	}
	statement := file.Stmts[0]
	call, ok := statement.Cmd.(*syntax.CallExpr)
	if !ok || statement.Negated || statement.Background || statement.Coprocess || len(statement.Redirs) != 0 || len(call.Assigns) != 0 || len(call.Args) == 0 {
		return exploreNone
	}
	args := make([]string, 0, len(call.Args))
	for _, word := range call.Args {
		value, ok := exploreLiteral(word)
		if !ok {
			return exploreNone
		}
		args = append(args, value)
	}
	for _, arg := range args[1:] {
		name, _, _ := strings.Cut(arg, "=")
		if exploreUnsupportedFlags[name] {
			return exploreNone
		}
	}
	program := path.Base(args[0])
	if (program == "npx" || program == "bunx" || program == "uvx") && len(args) > 1 {
		args = args[1:]
		program = path.Base(args[0])
	}
	if program == "man" && len(args) > 1 || slices.Contains(args[1:], "--help") || len(args) > 1 && args[1] == "help" {
		return exploreHelp
	}
	if explorePathPrograms[program] {
		return explorePaths
	}
	if subcommand, ok := exploreDiagnosticPrograms[program]; ok {
		if subcommand == "" || len(args) > 1 && args[1] == subcommand {
			return exploreDiagnostics
		}
		return exploreNone
	}
	switch program {
	case "go":
		if len(args) > 1 && (args[1] == "vet" || args[1] == "build") {
			return exploreDiagnostics
		}
	case "git":
		switch exploreGitSubcommand(args[1:]) {
		case "grep", "ls-files":
			return explorePaths
		case "log", "reflog":
			return exploreCommits
		case "diff", "show":
			return exploreDiffs
		}
	}
	return exploreNone
}

// exploreGitSubcommand skips global options, which never change the format.
func exploreGitSubcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "-C" || arg == "-c":
			i++
		case arg == "--no-pager" || strings.HasPrefix(arg, "--git-dir=") || strings.HasPrefix(arg, "--work-tree=") || strings.HasPrefix(arg, "-c"):
		case strings.HasPrefix(arg, "-"):
			return ""
		default:
			return arg
		}
	}
	return ""
}

func exploreLiteral(word *syntax.Word) (string, bool) {
	var value strings.Builder
	for i, part := range word.Parts {
		switch part := part.(type) {
		case *syntax.Lit:
			// Only a leading tilde expands, so `HEAD~8` stays literal.
			if strings.ContainsAny(part.Value, "*?[") || i == 0 && strings.HasPrefix(part.Value, "~") {
				return "", false
			}
			value.WriteString(part.Value)
		case *syntax.SglQuoted:
			if part.Dollar {
				return "", false
			}
			value.WriteString(part.Value)
		case *syntax.DblQuoted:
			for _, inner := range part.Parts {
				lit, ok := inner.(*syntax.Lit)
				if !ok {
					return "", false
				}
				value.WriteString(lit.Value)
			}
		default:
			return "", false
		}
	}
	return value.String(), true
}

// exploreExitAccepted reports whether a result state carries complete output
// for the family. Diagnostic tools report findings with a failing status.
func exploreExitAccepted(family exploreFamily, state string) bool {
	switch state {
	case "Process exited with code 0":
		return true
	case "Process exited with code 1", "Process exited with code 2":
		return family == exploreDiagnostics
	}
	return false
}

// filter returns the replacement output text, or false to keep the stock one.
func (f *exploreFilter) filter(ctx context.Context, task exploreTask, text string, store *mekugiReplayStore) (string, bool) {
	started := time.Now()
	debug, _ := ctx.Value(debugContextKey{}).(*debugOutput)
	outcome := func(reason string, fields map[string]any) {
		if debug == nil {
			return
		}
		fields["event"], fields["outcome"], fields["elapsed_ms"] = "explore_filter", reason, time.Since(started).Milliseconds()
		debug.event(fields)
	}
	family := exploreCommand(task.Command)
	if family == exploreNone {
		return "", false
	}
	state, body := nativeExecutionHeader(text)
	if !exploreExitAccepted(family, state) || len(body) < exploreMinBytes {
		return "", false
	}
	header := text[:len(text)-len(body)]
	rows := strings.SplitAfter(body, "\n")
	if rows[len(rows)-1] == "" {
		rows = rows[:len(rows)-1]
	}
	units, kind := exploreSplit(family, rows, task.Workdir)
	fields := map[string]any{"family": exploreFamilyNames[family], "rows": len(rows), "bytes": len(body), "units": len(units)}
	if len(units) < exploreMinUnits || len(units) > exploreMaxUnits {
		outcome("ineligible", fields)
		return "", false
	}
	probabilities, usage, err := f.judgeUnits(ctx, task, units, kind)
	fields["input_tokens"] = usage.InputTokens
	if err != nil {
		fields["error"] = err.Error()
		outcome("judge_error", fields)
		return "", false
	}
	ranked := make([]int, len(units))
	for i := range ranked {
		ranked[i] = i
	}
	slices.SortStableFunc(ranked, func(a, b int) int { return cmp.Compare(probabilities[b], probabilities[a]) })
	keep := make([]bool, len(units))
	for rank, unit := range ranked {
		keep[unit] = rank < exploreKeepTop || probabilities[unit] >= exploreKeepMin
	}
	dropRow := make([]bool, len(rows))
	var omitted []string
	omittedRows, omittedBytes := 0, 0
	for i, unit := range units {
		if keep[i] {
			continue
		}
		for _, row := range unit.rows {
			dropRow[row] = true
			omittedBytes += len(rows[row])
		}
		omittedRows += len(unit.rows)
		omitted = append(omitted, fmt.Sprintf("%s (%d)", unit.label(kind, rows), len(unit.rows)))
	}
	// The omission list is bounded by what it replaces: listing one-row units
	// in full would cost as much as the rows themselves.
	budget, listedBytes, listed := min(omittedBytes/4, exploreListedBytes), 0, 0
	for _, label := range omitted {
		if listedBytes+len(label)+2 > budget {
			break
		}
		listedBytes += len(label) + 2
		listed++
	}
	summary := strings.Join(omitted[:listed], ", ")
	if extra := len(omitted) - listed; extra > 0 && listed > 0 {
		summary += fmt.Sprintf(", +%d more", extra)
	} else if extra > 0 {
		summary = fmt.Sprintf("%d %s", extra, exploreUnitNouns[kind])
	}
	footer := fmt.Sprintf("[mekugi explore filter: omitted %d of %d %s (%d of %d lines) judged unrelated to the task: %s. Full output: mread ",
		len(omitted), len(units), exploreUnitNouns[kind], omittedRows, len(rows), summary)
	saved := omittedBytes - len(footer) - 16
	fields["omitted_units"], fields["omitted_rows"], fields["omitted_bytes"], fields["saved_bytes"] = len(omitted), omittedRows, omittedBytes, saved
	if saved < exploreMinSavedBytes || float64(saved) < exploreMinSavedShare*float64(len(body)) {
		outcome("kept", fields)
		return "", false
	}
	if store == nil {
		outcome("store_unavailable", fields)
		return "", false
	}
	reference, err := store.putTypedOutput(ctx, toolplugin.OmittedOutput{Stdout: body, StdoutKind: mrunRetainedKind(body)}, 0)
	if err != nil {
		outcome("store_error", fields)
		return "", false
	}
	var filtered strings.Builder
	filtered.WriteString(header)
	for i, row := range rows {
		if !dropRow[i] {
			filtered.WriteString(row)
		}
	}
	if !strings.HasSuffix(filtered.String(), "\n") {
		filtered.WriteByte('\n')
	}
	filtered.WriteString(footer + reference + "]\n")
	outcome("filtered", fields)
	return filtered.String(), true
}

var exploreFamilyNames = map[exploreFamily]string{explorePaths: "paths", exploreDiagnostics: "diagnostics", exploreCommits: "commits", exploreDiffs: "diffs", exploreHelp: "help"}

var exploreUnitNouns = map[exploreUnitKind]string{exploreFileUnit: "files", exploreDirectoryUnit: "directories", exploreCommitUnit: "commits", exploreDiffUnit: "file diffs", exploreHelpUnit: "help entries"}

// exploreSplit groups rows into units. Rows outside every unit, such as
// diagnostics, headings, or truncation markers, are always kept.
func exploreSplit(family exploreFamily, rows []string, workdir string) ([]exploreUnit, exploreUnitKind) {
	switch family {
	case exploreCommits:
		return exploreCommitUnits(rows), exploreCommitUnit
	case exploreDiffs:
		return exploreDiffUnits(rows), exploreDiffUnit
	case exploreHelp:
		return exploreHelpUnits(rows), exploreHelpUnit
	}
	units, byDirectory := explorePathUnits(rows, workdir)
	if byDirectory {
		return units, exploreDirectoryUnit
	}
	return units, exploreFileUnit
}

// explorePathUnits groups rows by the existing path each starts with. Indented
// rows continue the previous finding, as diagnostic code excerpts do. Large
// path sets group by parent directory.
func explorePathUnits(rows []string, workdir string) ([]exploreUnit, bool) {
	exists := make(map[string]bool)
	pathExists := func(candidate string) bool {
		if candidate == "" {
			return false
		}
		if known, ok := exists[candidate]; ok {
			return known
		}
		resolved := candidate
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(workdir, resolved)
		}
		_, err := os.Lstat(resolved)
		exists[candidate] = err == nil
		return err == nil
	}
	var units []exploreUnit
	index := make(map[string]int)
	last := -1
	for row, text := range rows {
		line := strings.TrimSuffix(text, "\n")
		key, rest := "", ""
		switch {
		case last >= 0 && (line == "--" || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")):
			// Context separators and excerpts belong to the group they follow.
			units[last].rows = append(units[last].rows, row)
			continue
		case pathExists(line):
			key = line
		default:
			if candidate, after, ok := strings.Cut(line, ":"); ok && pathExists(candidate) {
				key, rest = candidate, after
			} else if match := exploreParenRow.FindStringSubmatch(line); match != nil && pathExists(match[1]) {
				key, rest = match[1], line[len(match[1]):]
			} else if match := exploreContextRow.FindStringSubmatch(line); match != nil && pathExists(match[1]) {
				key, rest = match[1], line[len(match[1])+1:]
			}
		}
		if key == "" {
			last = -1
			continue
		}
		at, ok := index[key]
		if !ok {
			at = len(units)
			index[key] = at
			units = append(units, exploreUnit{key: key})
		}
		units[at].rows = append(units[at].rows, row)
		units[at].lines = append(units[at].lines, rest)
		last = at
	}
	if len(units) <= exploreMaxFileUnits {
		return units, false
	}
	var directories []exploreUnit
	index = make(map[string]int)
	for _, unit := range units {
		directory := filepath.Dir(unit.key)
		at, ok := index[directory]
		if !ok {
			at = len(directories)
			index[directory] = at
			directories = append(directories, exploreUnit{key: directory})
		}
		group := &directories[at]
		group.rows = append(group.rows, unit.rows...)
		for _, line := range unit.lines {
			entry := filepath.Base(unit.key)
			if line != "" {
				entry += ":" + line
			}
			group.lines = append(group.lines, entry)
		}
	}
	return directories, true
}

// exploreRowUnits starts a unit at every row matching start. Rows before the
// first unit, and rows matching pinned until the next start, stay in no unit.
func exploreRowUnits(rows []string, start func(string) bool, pinned func(string) bool) []exploreUnit {
	var units []exploreUnit
	current := -1
	for row, text := range rows {
		line := strings.TrimSuffix(text, "\n")
		switch {
		case start(line):
			units = append(units, exploreUnit{key: line})
			current = len(units) - 1
		case pinned != nil && pinned(line):
			current = -1
			continue
		case current < 0:
			continue
		}
		units[current].rows = append(units[current].rows, row)
		units[current].lines = append(units[current].lines, line)
	}
	return units
}

// exploreCommitUnits splits full log entries at their `commit` rows, or
// one-line logs at every abbreviated hash.
func exploreCommitUnits(rows []string) []exploreUnit {
	first := ""
	for _, row := range rows {
		if first = strings.TrimSpace(row); first != "" {
			break
		}
	}
	if exploreCommitRow.MatchString(first) {
		return exploreRowUnits(rows, exploreCommitRow.MatchString, nil)
	}
	if exploreOnelineRow.MatchString(first) {
		return exploreRowUnits(rows, exploreOnelineRow.MatchString, nil)
	}
	return nil
}

// exploreDiffUnits splits at every file header. A `git show` commit header
// before its diffs stays in no unit.
func exploreDiffUnits(rows []string) []exploreUnit {
	return exploreRowUnits(rows, func(line string) bool { return strings.HasPrefix(line, "diff --git ") }, exploreCommitRow.MatchString)
}

// exploreHelpUnits makes each option paragraph a unit. Unindented rows, such
// as section headings, stay in no unit.
func exploreHelpUnits(rows []string) []exploreUnit {
	return exploreRowUnits(rows, exploreOptionRow.MatchString, func(line string) bool {
		return line != "" && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t")
	})
}

func clipExploreText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return strings.ToValidUTF8(text[:limit], "") + "…"
}

func (u exploreUnit) label(kind exploreUnitKind, rows []string) string {
	switch kind {
	case exploreDirectoryUnit:
		return u.key + "/"
	case exploreCommitUnit:
		if hash, ok := strings.CutPrefix(u.key, "commit "); ok {
			// Full entries: the abbreviated hash and the subject row.
			label := hash[:min(len(hash), 12)]
			for _, line := range u.lines[1:] {
				if strings.HasPrefix(line, "    ") && strings.TrimSpace(line) != "" {
					return clipExploreText(label+" "+strings.TrimSpace(line), exploreLabelChars)
				}
			}
			return label
		}
	case exploreDiffUnit:
		if _, target, ok := strings.Cut(u.key, " b/"); ok {
			return target
		}
	case exploreFileUnit:
		return u.key // A clipped path cannot be opened.
	}
	return clipExploreText(strings.TrimSpace(u.key), exploreLabelChars)
}

func (u exploreUnit) state(kind exploreUnitKind) exploreUnitState {
	var state exploreUnitState
	sample := u.lines
	limit := exploreSampleLines
	switch kind {
	case exploreFileUnit:
		state.Path = u.key
		if len(u.lines) > 1 || len(u.lines) == 1 && u.lines[0] != "" {
			state.MatchCount = len(u.lines)
		} else {
			sample = nil
		}
	case exploreDirectoryUnit:
		state.Directory, state.Entries = u.key, len(u.lines)
	case exploreDiffUnit:
		// Hunk headers and changed rows say what a file diff is about.
		state.Label, state.LineCount = u.label(kind, nil), len(u.rows)
		sample, limit = nil, exploreDiffSample
		for _, line := range u.lines[1:] {
			if strings.HasPrefix(line, "@@") || (strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-")) && !strings.HasPrefix(line, "+++") && !strings.HasPrefix(line, "---") {
				sample = append(sample, line)
			}
		}
	default:
		state.Label, state.LineCount = clipExploreText(strings.TrimSpace(u.key), exploreLabelChars*2), len(u.rows)
		sample = nil
		for _, line := range u.lines[1:] {
			if strings.TrimSpace(line) != "" {
				sample = append(sample, strings.TrimSpace(line))
			}
		}
	}
	for _, line := range sample[:min(len(sample), limit)] {
		state.Sample = append(state.Sample, clipExploreText(line, exploreSampleChars))
	}
	return state
}

// File results keep the wording measured on search output; other families
// use the general form measured on commits, diffs, and help entries.
var (
	exploreFileCriteria = map[string]any{
		"true":  "The result is in code, files, or text that the agent is looking for, or that the agent plausibly needs to read or change for this task.",
		"false": "The result is unrelated to the agent's goal: it only shares a word or pattern with the search, or lies in dependencies, vendored, generated, or data files the task does not concern.",
	}
	exploreGeneralCriteria = map[string]any{
		"true":  "The result is something the agent is looking for, or that the agent plausibly needs to read, run, or change for this task.",
		"false": "The result is unrelated to the agent's goal: it only shares a word or pattern with the search, or concerns a different subject, component, or file than the task.",
	}
)

// judgeUnits asks one Noul per unit. Small batches keep each state focused;
// every answer must arrive or the whole judgment fails.
func (f *exploreFilter) judgeUnits(ctx context.Context, task exploreTask, units []exploreUnit, kind exploreUnitKind) ([]float64, typesafeUsage, error) {
	ctx, cancel := context.WithTimeout(ctx, exploreJudgeTimeout)
	defer cancel()
	goal := "`task.agent_intent`, given `task.user_request`"
	if task.AgentIntent == "" {
		goal = "`task.user_request`"
	}
	criteria := exploreGeneralCriteria
	if kind == exploreFileUnit || kind == exploreDirectoryUnit {
		criteria = exploreFileCriteria
	}
	states := make([]exploreUnitState, len(units))
	for i, unit := range units {
		states[i] = unit.state(kind)
	}
	probabilities := make([]float64, len(units))
	var mu sync.Mutex
	var usage typesafeUsage
	var failure error
	slots := make(chan struct{}, exploreConcurrency)
	var wg sync.WaitGroup
	for start := 0; start < len(states); {
		end, chars := start, 0
		for end < len(states) && end-start < exploreBatchUnits {
			size := len(states[end].Path) + len(states[end].Directory) + len(states[end].Label)
			for _, line := range states[end].Sample {
				size += len(line)
			}
			if end > start && chars+size > exploreBatchChars {
				break
			}
			chars += size
			end++
		}
		batch, offset := states[start:end], start
		start = end
		wg.Go(func() {
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-ctx.Done():
				mu.Lock()
				failure = cmpError(failure, ctx.Err())
				mu.Unlock()
				return
			}
			questions := make(map[string]typesafeNoul, len(batch))
			for i := range batch {
				questions[fmt.Sprintf("r%d", i)] = typesafeNoul{
					Type:         "noul",
					Instructions: map[string]string{"question": fmt.Sprintf("Does the agent need to see `results[%d]` to make progress on %s?", i, goal)},
					Criteria:     criteria,
				}
			}
			answers, used, err := f.judge.nouls(ctx, map[string]any{"task": task, "results": batch}, questions)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failure = cmpError(failure, err)
				cancel()
				return
			}
			usage.InputTokens += used.InputTokens
			usage.OutputTokens += used.OutputTokens
			for i := range batch {
				probabilities[offset+i] = answers[fmt.Sprintf("r%d", i)]
			}
		})
	}
	wg.Wait()
	return probabilities, usage, failure
}

func cmpError(current, next error) error {
	if current != nil {
		return current
	}
	return next
}
