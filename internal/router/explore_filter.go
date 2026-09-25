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
	"strconv"
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
// judgeable units. A list of statements may combine several families.
type exploreFamily uint8

const (
	explorePaths   exploreFamily = 1 << iota
	exploreListing               // bare paths, such as `rg --files` or `find`
	exploreDiagnostics
	exploreCommits
	exploreDiffs
	exploreHelp

	exploreNone exploreFamily = 0
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
	exploreHunkRow    = regexp.MustCompile(`^@@ -[0-9]+(?:,([0-9]+))? \+[0-9]+(?:,([0-9]+))? @@`)
	exploreEntryRow   = regexp.MustCompile(`^(?:(?:Author|AuthorDate|Commit|CommitDate|Merge|Date):|    | \S.* \| | [0-9]+ files? changed)`)
	exploreLineCount  = regexp.MustCompile(`^(?:-n|--lines=|-)\+?[0-9]+$`)
	exploreRowFlags   = regexp.MustCompile(`^-[viFEPwSx]+$`)
	// A cluster of boolean rg/grep short flags including -l; attached values
	// such as -thtml or -g*.lock are not clusters.
	exploreListingShort = regexp.MustCompile(`^-[inwxvuFSsHNaPULrRhE]*l[inwxvuFSsHNaPULrRhE]*$`)
)

// Extended header rows between `diff --git` and the first hunk.
var exploreDiffHeaders = []string{"index ", "--- ", "+++ ", "new file mode ", "deleted file mode ", "old mode ", "new mode ", "similarity index ", "dissimilarity index ", "rename from ", "rename to ", "copy from ", "copy to ", "Binary files "}

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
	callID      string
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
	kind  exploreUnitKind
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
		outputOnly := false
		if codeMode {
			source := jsonString(call, "input")
			if len(source) > maxMekugiScriptBytes {
				continue
			}
			nested, ok := toolActivityUnwrapExec(source, true)
			if !ok {
				nested, ok = toolActivityUnwrapExecOutput(source)
				outputOnly = ok
			}
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
		task := exploreTask{callID: callID, UserRequest: userRequest, AgentIntent: exploreIntent(items, calls[callID]), Command: input.Command, Workdir: input.Workdir}
		wg.Go(func() {
			defer close(decision.done)
			if codeMode {
				decision.output = f.filterCodeMode(ctx, task, output, outputOnly, store)
				return
			}
			text, ok := decodeJSONString(output)
			if !ok {
				return
			}
			state, body := nativeExecutionHeader(text)
			exit, err := strconv.Atoi(strings.TrimPrefix(state, "Process exited with code "))
			if !strings.HasPrefix(state, "Process exited with code ") || err != nil {
				return
			}
			filtered, ok := f.filter(ctx, task, body, &exit, store)
			if ok {
				decision.output = mustMarshalJSON(text[:len(text)-len(body)] + filtered)
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

// exploreCommand accepts literal invocations of programs whose output splits
// into units. A pipeline may end in filters that keep rows intact, such as
// `| head -80`. Statements joined by `;`, `&&`, `||`, or newlines form a list:
// their outputs are concatenated without boundaries, so a list's units come
// only from rows that prove their own family. A list's other statements must
// be known readers, whose rows stay in no unit; any other program could print
// rows that look like units, such as build errors. Directory changes and
// compound commands change what later paths mean and make the whole command
// ineligible.
func exploreCommand(command string) (family exploreFamily, list bool) {
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil {
		return exploreNone, false
	}
	var statements []*syntax.Stmt
	var collect func(*syntax.Stmt)
	collect = func(statement *syntax.Stmt) {
		if binary, ok := statement.Cmd.(*syntax.BinaryCmd); ok && (binary.Op == syntax.AndStmt || binary.Op == syntax.OrStmt) &&
			!statement.Negated && !statement.Background && len(statement.Redirs) == 0 {
			collect(binary.X)
			collect(binary.Y)
			return
		}
		statements = append(statements, statement)
	}
	for _, statement := range file.Stmts {
		collect(statement)
	}
	list = len(statements) > 1
	barePaths := false
	for _, statement := range statements {
		statementFamily, role := exploreStatement(statement, list)
		switch role {
		case exploreUnsafe:
			return exploreNone, false
		case exploreOther:
			if list {
				return exploreNone, false
			}
		case explorePathReader:
			barePaths = true
		}
		family |= statementFamily
	}
	if barePaths && family&exploreListing != 0 {
		// A bare path may be `ls` output, so only `path:` rows prove a unit.
		family = family&^exploreListing | explorePaths
	}
	return family, list
}

// exploreRole says what a statement's rows mean for its command.
type exploreRole int

const (
	exploreUnsafe     exploreRole = iota // later paths or all output become unattributable
	exploreOther                         // rows of unknown shape, such as build errors
	exploreReader                        // content the agent asked to read
	explorePathReader                    // a reader that prints bare paths, such as `ls`
	exploreProducer                      // rows of the returned family
)

// Readers print requested content, never search or diagnostic rows.
var (
	exploreReaders       = map[string]bool{"cat": true, "mcat": true, "sed": true, "head": true, "tail": true, "nl": true, "printf": true, "echo": true, "pwd": true, "wc": true, "date": true, "true": true, "inspect_file": true, "msymbol": true, "mread": true, "mchanges": true, "skills-mgr": true}
	explorePathReaders   = map[string]bool{"ls": true, "tree": true, "which": true, "command": true, "realpath": true, "readlink": true}
	exploreGitReaders    = map[string]bool{"status": true, "branch": true, "rev-parse": true, "worktree": true, "remote": true}
	exploreDiffSummaries = map[string]bool{"--check": true, "--stat": true, "--name-only": true, "--name-status": true, "--numstat": true, "--shortstat": true}
)

// exploreStatement returns the family of one statement's rows and its role.
func exploreStatement(statement *syntax.Stmt, list bool) (exploreFamily, exploreRole) {
	if statement.Background || statement.Coprocess {
		return exploreNone, exploreUnsafe
	}
	var stages []*syntax.Stmt
	var flatten func(*syntax.Stmt)
	flatten = func(current *syntax.Stmt) {
		if binary, ok := current.Cmd.(*syntax.BinaryCmd); ok && (binary.Op == syntax.Pipe || binary.Op == syntax.PipeAll) && len(current.Redirs) == 0 {
			flatten(binary.X)
			flatten(binary.Y)
			return
		}
		stages = append(stages, current)
	}
	flatten(statement)
	pipelines := make([][]string, 0, len(stages))
	for _, stage := range stages {
		call, ok := stage.Cmd.(*syntax.CallExpr)
		if !ok {
			return exploreNone, exploreUnsafe
		}
		if len(call.Args) == 0 {
			return exploreNone, exploreOther
		}
		// A dynamic program or a directory change, even through `builtin`,
		// changes what every later path means.
		program, ok := exploreLiteral(call.Args[0])
		if ok && slices.Contains([]string{"builtin", "command", "exec"}, program) && len(call.Args) > 1 {
			program, ok = exploreLiteral(call.Args[1])
		}
		if !ok || slices.Contains([]string{"cd", "pushd", "popd", "eval", "source", "."}, program) {
			return exploreNone, exploreUnsafe
		}
		args := make([]string, 0, len(call.Args))
		for _, word := range call.Args {
			value, ok := exploreLiteral(word)
			if !ok {
				return exploreNone, exploreOther
			}
			args = append(args, value)
		}
		if stage.Negated || len(call.Assigns) != 0 || !exploreStderrRedirects(stage.Redirs) {
			return exploreNone, exploreOther
		}
		pipelines = append(pipelines, args)
	}
	first := pipelines[0]
	program := path.Base(first[0])
	if exploreReaders[program] || explorePathReaders[program] ||
		program == "git" && (exploreGitReaders[exploreGitSubcommand(first[1:])] ||
			slices.Contains([]string{"diff", "show"}, exploreGitSubcommand(first[1:])) && slices.ContainsFunc(first[1:], func(arg string) bool { return exploreDiffSummaries[arg] })) {
		for _, stage := range pipelines[1:] {
			if !exploreReaders[path.Base(stage[0])] && !exploreRowFilter(stage) {
				return exploreNone, exploreOther
			}
		}
		if explorePathReaders[program] {
			return exploreNone, explorePathReader
		}
		return exploreNone, exploreReader
	}
	family := exploreInvocation(first)
	for _, filter := range pipelines[1:] {
		// A filtered file diff, log entry, or help paragraph loses the rows
		// that bound it; only a single command's trailing cut is harmless.
		multiRow := family&(exploreDiffs|exploreCommits|exploreHelp) != 0
		if !exploreRowFilter(filter) || multiRow && (list || path.Base(filter[0]) != "head") {
			return exploreNone, exploreOther
		}
	}
	if family == exploreNone {
		return exploreNone, exploreOther
	}
	return family, exploreProducer
}

// exploreStderrRedirects accepts only redirections that leave stdout rows in
// place: `2>&1` and `2>/dev/null`.
func exploreStderrRedirects(redirects []*syntax.Redirect) bool {
	for _, redirect := range redirects {
		if redirect.N == nil || redirect.N.Value != "2" || redirect.Word == nil {
			return false
		}
		target := redirect.Word.Lit()
		if !(redirect.Op == syntax.DplOut && target == "1" || redirect.Op == syntax.RdrOut && target == "/dev/null") {
			return false
		}
	}
	return true
}

// exploreRowFilter accepts a pipeline stage that only selects or reorders
// whole rows of its input.
func exploreRowFilter(args []string) bool {
	switch path.Base(args[0]) {
	case "head", "tail":
		for i := 1; i < len(args); i++ {
			if args[i] == "-n" && i+1 < len(args) {
				i++
				if !exploreLineCount.MatchString("-n" + args[i]) {
					return false
				}
			} else if !exploreLineCount.MatchString(args[i]) {
				return false
			}
		}
		return true
	case "sort":
		for _, arg := range args[1:] {
			if strings.Trim(arg, "-urnVf") != "" || !strings.HasPrefix(arg, "-") {
				return false
			}
		}
		return true
	case "uniq":
		return len(args) == 1
	case "rg", "grep":
		patterns := 0
		for i := 1; i < len(args); i++ {
			switch arg := args[i]; {
			case arg == "-e" && i+1 < len(args):
				i++
				patterns++
			case strings.HasPrefix(arg, "-"):
				if !exploreRowFlags.MatchString(arg) {
					return false
				}
			default:
				patterns++
			}
		}
		// A second operand would be a file, not the piped rows.
		return patterns == 1
	}
	return false
}

// exploreInvocation classifies one program invocation.
func exploreInvocation(args []string) exploreFamily {
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
		if program != "rg" && program != "grep" && program != "egrep" && program != "fgrep" || slices.ContainsFunc(args[1:], exploreListingFlag) {
			return exploreListing
		}
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
		case "grep":
			if slices.ContainsFunc(args[1:], exploreListingFlag) {
				return exploreListing
			}
			return explorePaths
		case "ls-files":
			return exploreListing
		case "log", "reflog":
			return exploreCommits
		case "diff", "show":
			return exploreDiffs
		}
	}
	return exploreNone
}

// exploreListingFlag reports a search flag that prints matching file names
// instead of matching rows.
func exploreListingFlag(arg string) bool {
	switch arg {
	case "--files", "--files-with-matches", "--files-without-match", "--name-only":
		return true
	}
	return exploreListingShort.MatchString(arg)
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

// exploreLiteral returns a word's text when its expansion cannot change which
// program runs or which flags it sees. Globs, a leading tilde, and `$HOME`
// expand to paths and are kept as written.
func exploreLiteral(word *syntax.Word) (string, bool) {
	var value strings.Builder
	home := func(part *syntax.ParamExp) bool {
		var printed strings.Builder
		return syntax.NewPrinter().Print(&printed, part) == nil && (printed.String() == "$HOME" || printed.String() == "${HOME}")
	}
	for _, part := range word.Parts {
		switch part := part.(type) {
		case *syntax.Lit:
			value.WriteString(part.Value)
		case *syntax.SglQuoted:
			if part.Dollar {
				return "", false
			}
			value.WriteString(part.Value)
		case *syntax.DblQuoted:
			for _, inner := range part.Parts {
				switch inner := inner.(type) {
				case *syntax.Lit:
					value.WriteString(inner.Value)
				case *syntax.ParamExp:
					if !home(inner) {
						return "", false
					}
					value.WriteString("$HOME")
				default:
					return "", false
				}
			}
		case *syntax.ParamExp:
			if !home(part) {
				return "", false
			}
			value.WriteString("$HOME")
		default:
			return "", false
		}
	}
	return value.String(), true
}

// exploreExitAccepted reports whether an exit status carries complete output
// for the family. Diagnostic tools report findings with a failing status. A
// list reports only its last statement's status and printed stdout carries
// none, so neither limits what its rows prove.
func exploreExitAccepted(family exploreFamily, list bool, exit *int) bool {
	if exit == nil || list {
		return true
	}
	switch *exit {
	case 0:
		return true
	case 1, 2:
		return family == exploreDiagnostics
	}
	return false
}

// filter returns the replacement for a command's output body, or false to
// keep the stock one. A nil exit means the status was not printed.
func (f *exploreFilter) filter(ctx context.Context, task exploreTask, body string, exit *int, store *mekugiReplayStore) (string, bool) {
	started := time.Now()
	debug, _ := ctx.Value(debugContextKey{}).(*debugOutput)
	outcome := func(reason string, fields map[string]any) {
		if debug == nil {
			return
		}
		fields["event"], fields["outcome"], fields["elapsed_ms"] = "explore_filter", reason, time.Since(started).Milliseconds()
		debug.event(fields)
	}
	family, list := exploreCommand(task.Command)
	if family == exploreNone || !exploreExitAccepted(family, list, exit) || len(body) < exploreMinBytes {
		return "", false
	}
	rows := strings.SplitAfter(body, "\n")
	if rows[len(rows)-1] == "" {
		rows = rows[:len(rows)-1]
	}
	var units []exploreUnit
	if list {
		units = exploreListUnits(family, rows, task.Workdir)
	} else {
		units = exploreSplit(family, rows, task.Workdir)
	}
	unitBytes := 0
	for _, unit := range units {
		for _, row := range unit.rows {
			unitBytes += len(rows[row])
		}
	}
	fields := map[string]any{"family": family.String(), "list": list, "rows": len(rows), "bytes": len(body), "units": len(units), "unit_bytes": unitBytes}
	if len(units) < exploreMinUnits || len(units) > exploreMaxUnits {
		outcome("ineligible", fields)
		return "", false
	}
	probabilities, usage, err := f.judgeUnits(ctx, task, units)
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
		omitted = append(omitted, fmt.Sprintf("%s (%d)", unit.label(), len(unit.rows)))
	}
	noun := exploreUnitNouns[units[0].kind]
	for _, unit := range units {
		if exploreUnitNouns[unit.kind] != noun {
			noun = "results"
			break
		}
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
		summary = fmt.Sprintf("%d %s", extra, noun)
	}
	footer := fmt.Sprintf("[mekugi explore filter: omitted %d of %d %s (%d of %d lines) judged unrelated to the task: %s. Full output: mread ",
		len(omitted), len(units), noun, omittedRows, len(rows), summary)
	saved := omittedBytes - len(footer) - 16
	fields["omitted_units"], fields["omitted_rows"], fields["omitted_bytes"], fields["saved_bytes"] = len(omitted), omittedRows, omittedBytes, saved
	// The share counts judged rows only, so a list's other statements do not
	// hide savings in its search results.
	if saved < exploreMinSavedBytes || float64(saved) < exploreMinSavedShare*float64(unitBytes) {
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
	for i, row := range rows {
		if !dropRow[i] {
			filtered.WriteString(row)
		}
	}
	if filtered.Len() > 0 && !strings.HasSuffix(filtered.String(), "\n") {
		filtered.WriteByte('\n')
	}
	filtered.WriteString(footer + reference + "]\n")
	if observer, ok := ctx.Value(exploreObserverKey{}).(exploreObserver); ok && observer.filtered != nil {
		observer.filtered(task, body, filtered.String(), exploreFilterEvent{
			Family: family.String(), LinesBefore: len(rows), LinesRemoved: omittedRows,
			UnitsBefore: len(units), UnitsRemoved: len(omitted), JudgeUsage: usage,
			ElapsedMS: time.Since(started).Milliseconds(),
		})
	}
	outcome("filtered", fields)
	return filtered.String(), true
}

var exploreFamilyNames = []string{"paths", "listing", "diagnostics", "commits", "diffs", "help"}

// String names the families joined by "+", such as "paths+diffs".
func (f exploreFamily) String() string {
	var names []string
	for i, name := range exploreFamilyNames {
		if f&(1<<i) != 0 {
			names = append(names, name)
		}
	}
	return strings.Join(names, "+")
}

var exploreUnitNouns = map[exploreUnitKind]string{exploreFileUnit: "files", exploreDirectoryUnit: "directories", exploreCommitUnit: "commits", exploreDiffUnit: "file diffs", exploreHelpUnit: "help entries"}

// exploreSplit groups rows into units. Rows outside every unit, such as
// diagnostics, headings, or truncation markers, are always kept.
func exploreSplit(family exploreFamily, rows []string, workdir string) []exploreUnit {
	var units []exploreUnit
	var kind exploreUnitKind
	switch family {
	case exploreCommits:
		units, kind = exploreCommitUnits(rows), exploreCommitUnit
	case exploreDiffs:
		units, kind = exploreDiffUnits(rows), exploreDiffUnit
	case exploreHelp:
		units, kind = exploreHelpUnits(rows), exploreHelpUnit
	default:
		return explorePathUnits(rows, workdir)
	}
	for i := range units {
		units[i].kind = kind
	}
	return units
}

// explorePathKeys resolves the existing path a row starts with, caching
// lookups relative to the working directory.
func explorePathKeys(workdir string) func(line string) (key, rest string) {
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
	return func(line string) (string, string) {
		if pathExists(line) {
			return line, ""
		}
		if candidate, after, ok := strings.Cut(line, ":"); ok && pathExists(candidate) {
			return candidate, after
		}
		if match := exploreParenRow.FindStringSubmatch(line); match != nil && pathExists(match[1]) {
			return match[1], line[len(match[1]):]
		}
		if match := exploreContextRow.FindStringSubmatch(line); match != nil && pathExists(match[1]) {
			return match[1], line[len(match[1])+1:]
		}
		return "", ""
	}
}

// explorePathUnits groups rows by the existing path each starts with. Indented
// rows continue the previous finding, as diagnostic code excerpts do. Large
// path sets group by parent directory.
func explorePathUnits(rows []string, workdir string) []exploreUnit {
	pathKey := explorePathKeys(workdir)
	var units []exploreUnit
	index := make(map[string]int)
	last := -1
	for row, text := range rows {
		line := strings.TrimSuffix(text, "\n")
		if last >= 0 && (line == "--" || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) {
			// Context separators and excerpts belong to the group they follow.
			units[last].rows = append(units[last].rows, row)
			continue
		}
		key, rest := pathKey(line)
		if key == "" {
			last = -1
			continue
		}
		last = explorePathRow(&units, index, key, rest, row)
	}
	return exploreGroupDirectories(units)
}

// explorePathRow adds a row to the file unit for key and returns its index.
func explorePathRow(units *[]exploreUnit, index map[string]int, key, rest string, row int) int {
	at, ok := index[key]
	if !ok {
		at = len(*units)
		index[key] = at
		*units = append(*units, exploreUnit{key: key, kind: exploreFileUnit})
	}
	unit := &(*units)[at]
	unit.rows = append(unit.rows, row)
	unit.lines = append(unit.lines, rest)
	return at
}

// exploreGroupDirectories replaces more than exploreMaxFileUnits file units
// with one unit per parent directory, keeping other units in place.
func exploreGroupDirectories(units []exploreUnit) []exploreUnit {
	files := 0
	for _, unit := range units {
		if unit.kind == exploreFileUnit {
			files++
		}
	}
	if files <= exploreMaxFileUnits {
		return units
	}
	var grouped []exploreUnit
	index := make(map[string]int)
	for _, unit := range units {
		if unit.kind != exploreFileUnit {
			grouped = append(grouped, unit)
			continue
		}
		directory := filepath.Dir(unit.key)
		at, ok := index[directory]
		if !ok {
			at = len(grouped)
			index[directory] = at
			grouped = append(grouped, exploreUnit{key: directory, kind: exploreDirectoryUnit})
		}
		group := &grouped[at]
		group.rows = append(group.rows, unit.rows...)
		for _, line := range unit.lines {
			entry := filepath.Base(unit.key)
			if line != "" {
				entry += ":" + line
			}
			group.lines = append(group.lines, entry)
		}
	}
	for i := range grouped {
		slices.Sort(grouped[i].rows)
	}
	return grouped
}

// exploreListUnits splits the concatenated output of a list's statements.
// Nothing marks where one statement's output ends, so only rows that prove
// their family form units: rows led by an existing path, file diffs bounded
// by their hunk line counts, and full commit entries. One-line commits are
// indistinguishable from `git blame` rows. A bare path forms a unit only
// when a statement lists paths, and indented rows never continue a path unit:
// either may be another statement's output, such as `ls` or file content.
func exploreListUnits(family exploreFamily, rows []string, workdir string) []exploreUnit {
	pathKey := explorePathKeys(workdir)
	var units []exploreUnit
	index := make(map[string]int)
	last := -1
	for row := 0; row < len(rows); {
		line := strings.TrimSuffix(rows[row], "\n")
		end, kind := row, exploreFileUnit
		switch {
		case family&exploreDiffs != 0 && strings.HasPrefix(line, "diff --git "):
			end, kind = exploreDiffEnd(rows, row), exploreDiffUnit
		case family&exploreCommits != 0 && exploreCommitRow.MatchString(line):
			end, kind = exploreCommitEnd(rows, row), exploreCommitUnit
		case family&(explorePaths|exploreListing|exploreDiagnostics) != 0:
			if last >= 0 && line == "--" {
				units[last].rows = append(units[last].rows, row)
				row++
				continue
			}
			if key, rest := pathKey(line); key != "" && (rest != "" || family&exploreListing != 0) {
				last = explorePathRow(&units, index, key, rest, row)
				row++
				continue
			}
		}
		last = -1
		if end == row {
			row++
			continue
		}
		unit := exploreUnit{key: line, kind: kind}
		for ; row < end; row++ {
			unit.rows = append(unit.rows, row)
			unit.lines = append(unit.lines, strings.TrimSuffix(rows[row], "\n"))
		}
		units = append(units, unit)
	}
	return exploreGroupDirectories(units)
}

// exploreDiffEnd returns the row after the file diff starting at start: its
// extended headers, then hunks whose rows match their header's line counts.
func exploreDiffEnd(rows []string, start int) int {
	end := start + 1
	for end < len(rows) && slices.ContainsFunc(exploreDiffHeaders, func(prefix string) bool { return strings.HasPrefix(rows[end], prefix) }) {
		end++
	}
	for end < len(rows) {
		match := exploreHunkRow.FindStringSubmatch(rows[end])
		if match == nil {
			return end
		}
		count := func(value string) int {
			if value == "" {
				return 1
			}
			n, _ := strconv.Atoi(value)
			return n
		}
		removed, added := count(match[1]), count(match[2])
		for end++; end < len(rows) && (removed > 0 || added > 0 || strings.HasPrefix(rows[end], "\\")); end++ {
			switch rows[end][0] {
			case ' ':
				removed, added = removed-1, added-1
			case '-':
				removed--
			case '+':
				added--
			case '\\': // No newline at end of file.
			default:
				return end
			}
		}
	}
	return end
}

// exploreCommitEnd returns the row after the full log entry starting at start:
// its header fields, indented message, stat rows, and any file diffs.
func exploreCommitEnd(rows []string, start int) int {
	end := start + 1
	for end < len(rows) {
		line := strings.TrimSuffix(rows[end], "\n")
		switch {
		case line == "" || exploreEntryRow.MatchString(line):
			end++
		case strings.HasPrefix(line, "diff --git "):
			end = exploreDiffEnd(rows, end)
		default:
			return end
		}
	}
	return end
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

func (u exploreUnit) label() string {
	switch u.kind {
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

func (u exploreUnit) state() exploreUnitState {
	var state exploreUnitState
	sample := u.lines
	limit := exploreSampleLines
	switch u.kind {
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
		state.Label, state.LineCount = u.label(), len(u.rows)
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
func (f *exploreFilter) judgeUnits(ctx context.Context, task exploreTask, units []exploreUnit) ([]float64, typesafeUsage, error) {
	ctx, cancel := context.WithTimeout(ctx, exploreJudgeTimeout)
	defer cancel()
	goal := "`task.agent_intent`, given `task.user_request`"
	if task.AgentIntent == "" {
		goal = "`task.user_request`"
	}
	states := make([]exploreUnitState, len(units))
	for i, unit := range units {
		states[i] = unit.state()
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
				criteria := exploreGeneralCriteria
				if kind := units[offset+i].kind; kind == exploreFileUnit || kind == exploreDirectoryUnit {
					criteria = exploreFileCriteria
				}
				questions[fmt.Sprintf("r%d", i)] = typesafeNoul{
					Type:         "noul",
					Instructions: map[string]string{"question": fmt.Sprintf("Does the agent need to see `results[%d]` to make progress on %s?", i, goal)},
					Criteria:     criteria,
				}
			}
			answers, used, err := f.judge.nouls(ctx, map[string]any{"task": task, "results": batch}, questions)
			if observer, ok := ctx.Value(exploreObserverKey{}).(exploreObserver); ok && observer.usage != nil {
				observer.usage(used)
			}
			mu.Lock()
			defer mu.Unlock()
			usage.add(used)
			if err != nil {
				failure = cmpError(failure, err)
				cancel()
				return
			}
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
