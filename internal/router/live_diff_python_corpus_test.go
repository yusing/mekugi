package router

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestLiveDiffPythonCorpusCoverage measures how many inline Python edit
// scripts from recorded Claude and Codex sessions the interpreter preview
// predicts, and why the rest stop. It reads local session logs, so it runs
// only when MEKUGI_PYTHON_CORPUS is set:
//
//	MEKUGI_PYTHON_CORPUS=1 go test ./internal/router -run PythonCorpus -v
//
// MEKUGI_PYTHON_CORPUS_ROOTS (path-list separated) overrides the default
// ~/.claude/projects and ~/.codex/sessions; MEKUGI_PYTHON_CORPUS_REPORT names
// a Markdown report file. MEKUGI_PYTHON_CORPUS_FREEZE saves a private mode-0600 eligible population;
// MEKUGI_PYTHON_CORPUS_FREEZE_ONLY skips projection after freezing, and
// MEKUGI_PYTHON_CORPUS_FROZEN replays it without rescanning sessions.
// Previews only read the synthetic sources seeded
// below and never execute a script, so the report is deterministic for a
// fixed corpus.
func TestLiveDiffPythonCorpusCoverage(t *testing.T) {
	if os.Getenv("MEKUGI_PYTHON_CORPUS") == "" {
		t.Skip("set MEKUGI_PYTHON_CORPUS to measure recorded Python edit scripts")
	}
	roots := filepath.SplitList(os.Getenv("MEKUGI_PYTHON_CORPUS_ROOTS"))
	if len(roots) == 0 {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		roots = []string{filepath.Join(home, ".claude", "projects"), filepath.Join(home, ".codex", "sessions")}
	}
	var commands []pythonCorpusCommand
	var populations map[string]*pythonCorpusPopulation
	var err error
	if os.Getenv("MEKUGI_PYTHON_CORPUS_FROZEN") == "" {
		commands, populations, err = pythonCorpusCommands(roots)
	}
	if err != nil {
		t.Fatal(err)
	}
	if path := os.Getenv("MEKUGI_PYTHON_CORPUS_FREEZE"); path != "" {
		if err := pythonCorpusSave(path, commands, populations); err != nil {
			t.Fatal(err)
		}
		t.Log("Frozen eligible corpus written with mode 0600")
	}
	if path := os.Getenv("MEKUGI_PYTHON_CORPUS_FROZEN"); path != "" {
		commands, populations, err = pythonCorpusLoad(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, agent := range slices.Sorted(maps.Keys(populations)) {
		t.Logf("Population %s: %+v; Excluded=%d", agent, *populations[agent], populations[agent].NonInvocation+populations[agent].Analysis)
	}
	if len(commands) == 0 {
		t.Skip("no inline Python edit scripts found")
	}
	// Identify the exact deduplicated population without exposing session text.
	// Sorting makes the fingerprint independent of log-file traversal order.
	keys := make([]string, 0, len(commands))
	for _, command := range commands {
		key := sha256.Sum256([]byte(command.agent + "\x00" + command.directory + "\x00" + command.command))
		keys = append(keys, fmt.Sprintf("%x", key))
	}
	slices.Sort(keys)
	t.Logf("Eligible corpus population: %d scripts, SHA256 %x", len(commands), sha256.Sum256([]byte(strings.Join(keys, "\n"))))
	if os.Getenv("MEKUGI_PYTHON_CORPUS_FREEZE_ONLY") != "" {
		return
	}
	type tally struct{ total, predicted, streamed int }
	totals := make(map[string]*tally)
	categories := make(map[string]map[string]*tally)
	reasons := make(map[string]map[string]int)
	samples := make(map[string]string)
	for _, command := range commands {
		outcome, streamed := pythonCorpusOutcome(t.Context(), command)
		total := totals[command.agent]
		if total == nil {
			total = &tally{}
			totals[command.agent] = total
		}
		if categories[command.agent] == nil {
			categories[command.agent] = make(map[string]*tally)
		}
		category := categories[command.agent][command.category]
		if category == nil {
			category = &tally{}
			categories[command.agent][command.category] = category
		}
		for _, count := range []*tally{total, category} {
			count.total++
			if outcome == "" {
				count.predicted++
				if streamed {
					count.streamed++
				}
			}
		}
		if outcome == "" {
			continue
		}
		if reasons[outcome] == nil {
			reasons[outcome] = make(map[string]int)
		}
		reasons[outcome][command.agent]++
		if _, exists := samples[outcome]; !exists {
			samples[outcome] = command.origin
		}
	}
	var report strings.Builder
	report.WriteString("Synthetic source coverage only, not historical execution correctness.\n\n# Python edit-script preview coverage\n\n")
	report.WriteString("| Agent | Raw candidates | Confirmed invocations | Eligible | Excluded non-invocations | Excluded analysis | Uncertain extraction | Unknown edit intent (eligible) | Python parse uncertainty (eligible) |\n| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |\n")
	for _, agent := range slices.Sorted(maps.Keys(populations)) {
		p := populations[agent]
		fmt.Fprintf(&report, "| %s | %d | %d | %d | %d | %d | %d | %d | %d |\n", agent, p.Raw, p.Invocations, p.Eligible, p.NonInvocation, p.Analysis, p.Uncertain, p.UnknownIntent, p.PythonParseUncertainty)
	}
	report.WriteString("\n| Agent | Scripts | Predicted | Streamed before completion |\n| --- | ---: | ---: | ---: |\n")
	for _, agent := range slices.Sorted(maps.Keys(totals)) {
		total := totals[agent]
		fmt.Fprintf(&report, "| %s | %d | %d (%.1f%%) | %d |\n", agent, total.total, total.predicted, 100*float64(total.predicted)/float64(total.total), total.streamed)
	}
	report.WriteString("\n## Coverage by edit intent\n\nUnknown-intent and parse-uncertain invocations remain in the broad denominator above, but are not confirmed edit scripts.\n\n| Agent | Intent | Scripts | Predicted | Streamed before completion |\n| --- | --- | ---: | ---: | ---: |\n")
	for _, agent := range slices.Sorted(maps.Keys(categories)) {
		for _, intent := range slices.Sorted(maps.Keys(categories[agent])) {
			count := categories[agent][intent]
			fmt.Fprintf(&report, "| %s | %s | %d | %d (%.1f%%) | %d |\n", agent, intent, count.total, count.predicted, 100*float64(count.predicted)/float64(count.total), count.streamed)
		}
	}
	report.WriteString("\n## Unpredicted scripts by reason\n\n| Count | Claude | Codex | Reason | First occurrence |\n| ---: | ---: | ---: | --- | --- |\n")
	ordered := slices.SortedFunc(maps.Keys(reasons), func(a, b string) int {
		count := func(reason string) int { return reasons[reason]["claude"] + reasons[reason]["codex"] }
		return cmp.Or(cmp.Compare(count(b), count(a)), strings.Compare(a, b))
	})
	for _, reason := range ordered {
		fmt.Fprintf(&report, "| %d | %d | %d | %s | %s |\n", reasons[reason]["claude"]+reasons[reason]["codex"], reasons[reason]["claude"], reasons[reason]["codex"],
			strings.ReplaceAll(reason, "|", `\|`), samples[reason])
	}
	if path := os.Getenv("MEKUGI_PYTHON_CORPUS_REPORT"); path != "" {
		if err := os.WriteFile(path, []byte(report.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Log("\n" + report.String())
}

type pythonCorpusCommand struct {
	agent, origin, directory, command string
	category                          string
}

var (
	pythonCorpusInline = regexp.MustCompile(`\bpython3?(\.\d+)?\b[^\n|;&]*(<<|\s-c\s)`)
	// Literals in the command text, used to seed synthetic target sources.
	pythonCorpusLiteral = regexp.MustCompile(`(?s)"""(.*?)"""|'''(.*?)'''|"((?:[^"\\\n]|\\.)*)"|'((?:[^'\\\n]|\\.)*)'`)
	pythonCorpusPath    = regexp.MustCompile(`^[\w./~@+-]+$`)
	pythonCorpusCd      = regexp.MustCompile(`(?:^|[;&|\n(]\s*)cd\s+([^\s;&|)]+)`)
)

// pythonCorpusCommands inventories deduplicated regex candidates, then extracts
// actual Python invocations structurally. Unknown edit intent stays eligible;
// uncertain invocation/source extraction remains visible outside the denominator.
func pythonCorpusCommands(roots []string) ([]pythonCorpusCommand, map[string]*pythonCorpusPopulation, error) {
	populations := make(map[string]*pythonCorpusPopulation)
	var files []string
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err == nil && !entry.IsDir() && strings.HasSuffix(path, ".jsonl") {
				files = append(files, path)
			}
			if os.IsNotExist(err) {
				return nil
			}
			return err
		})
		if err != nil {
			return nil, nil, err
		}
	}
	slices.Sort(files)
	seen := make(map[[32]byte]bool)
	var commands []pythonCorpusCommand
	for _, file := range files {
		handle, err := os.Open(file)
		if err != nil {
			return nil, nil, err
		}
		scanner := bufio.NewScanner(handle)
		scanner.Buffer(nil, 256<<20)
		for line := 1; scanner.Scan(); line++ {
			text := scanner.Bytes()
			if !bytes.Contains(text, []byte("python")) {
				continue
			}
			for _, command := range pythonCorpusEntry(text) {
				if !pythonCorpusInline.MatchString(command.command) {
					continue
				}
				key := sha256.Sum256([]byte(command.agent + "\x00" + command.directory + "\x00" + command.command))
				if seen[key] {
					continue
				}
				seen[key] = true
				command.origin = fmt.Sprintf("%s:%d", filepath.Base(file), line)
				population := populations[command.agent]
				if population == nil {
					population = &pythonCorpusPopulation{}
					populations[command.agent] = population
				}
				population.Raw++
				extracted, uncertainty := pythonCorpusExtractDetailed(command)
				uncertain := 0
				if population.Uncertainty == nil {
					population.Uncertainty = make(map[string]int)
				}
				for reason, count := range uncertainty {
					uncertain += count
					population.Uncertainty[reason] += count
				}
				population.Uncertain += uncertain
				if len(extracted) == 0 && uncertain == 0 {
					population.NonInvocation++
				}
				for _, invocation := range extracted {
					population.Invocations++
					switch invocation.category {
					case "analysis":
						population.Analysis++
					default:
						population.Eligible++
						if invocation.category == "Python parse uncertainty" {
							population.PythonParseUncertainty++
						}
						if invocation.category == "unknown edit intent" {
							population.UnknownIntent++
						}
						commands = append(commands, invocation)
					}
				}
			}
		}
		err = scanner.Err()
		handle.Close()
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", file, err)
		}
	}
	return commands, populations, nil
}

func pythonCorpusEntry(line []byte) []pythonCorpusCommand {
	var entry struct {
		Type    string `json:"type"`
		Cwd     string `json:"cwd"`
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
		Payload struct {
			Type string `json:"type"`
			Item struct {
				Type    string   `json:"type"`
				Command []string `json:"command"`
				Cwd     string   `json:"cwd"`
			} `json:"item"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &entry) != nil {
		return nil
	}
	if entry.Type == "assistant" {
		var content []struct {
			Type  string `json:"type"`
			Name  string `json:"name"`
			Input struct {
				Command string `json:"command"`
			} `json:"input"`
		}
		if json.Unmarshal(entry.Message.Content, &content) != nil {
			return nil
		}
		var commands []pythonCorpusCommand
		for _, block := range content {
			if block.Type == "tool_use" && block.Name == "Bash" && block.Input.Command != "" {
				commands = append(commands, pythonCorpusCommand{agent: "claude", directory: entry.Cwd, command: block.Input.Command})
			}
		}
		return commands
	}
	item := entry.Payload.Item
	if entry.Payload.Type == "item_completed" && item.Type == "CommandExecution" && len(item.Command) == 3 && (item.Command[1] == "-lc" || item.Command[1] == "-c") {
		return []pythonCorpusCommand{{agent: "codex", directory: strings.TrimPrefix(item.Cwd, "file://"), command: item.Command[2]}}
	}
	return nil
}

// pythonCorpusOutcome previews one command against synthetic sources. Every
// path-like literal, resolved from the working directory and each literal
// `cd` target, reads as a file holding every distinct literal of the command, so a
// literal replacement finds its old text. It returns "" for a prediction and
// otherwise the reason it stopped, and whether any unfinished input previewed.
func pythonCorpusOutcome(ctx context.Context, command pythonCorpusCommand) (string, bool) {
	if !filepath.IsAbs(command.directory) {
		return "no absolute working directory", false
	}
	var literals []string
	for _, match := range pythonCorpusLiteral.FindAllStringSubmatch(command.command, -1) {
		for _, group := range match[1:] {
			if group != "" {
				literals = append(literals, pythonCorpusUnescape(group))
			}
		}
	}
	directories := []string{command.directory}
	for _, match := range pythonCorpusCd.FindAllStringSubmatch(command.command, -1) {
		target := strings.Trim(match[1], `"'`)
		if !filepath.IsAbs(target) {
			target = filepath.Join(command.directory, target)
		}
		directories = append(directories, filepath.Clean(target))
	}
	// Literals keep their order of appearance, so markers that bound a slice
	// occur in order.
	var content strings.Builder
	written := make(map[string]bool)
	for _, literal := range literals {
		if !written[literal] {
			written[literal] = true
			content.WriteString(literal)
			content.WriteString("\n")
		}
	}
	sources := &liveDiffSources{capturedOnly: true, files: make(map[liveDiffSourceKey]liveDiffSource)}
	reader := reflect.ValueOf(liveDiffPreviewFile).Pointer()
	for _, literal := range literals {
		if !pythonCorpusPath.MatchString(literal) || !strings.ContainsAny(literal, "./") || strings.HasPrefix(literal, "~") {
			continue
		}
		for _, directory := range directories {
			path := literal
			if !filepath.IsAbs(path) {
				path = filepath.Join(directory, path)
			}
			sources.files[liveDiffSourceKey{reader, filepath.Clean(path)}] = liveDiffSource{content.String(), true}
		}
	}
	project := func(program string, final bool) ([]string, error) {
		var failure error
		frame := context.WithValue(context.WithValue(ctx, liveDiffSourcesContext{}, sources), liveDiffFailureContext{}, &failure)
		worker := liveDiffPreviewWorker{ctx: frame}
		files, _, err := worker.projectShell(program, command.directory, final)
		var paths []string
		for _, file := range files {
			if file.Incomplete == "" && file.Diff != "" {
				paths = append(paths, cmp.Or(file.AfterPath, file.BeforePath))
			}
		}
		return paths, cmp.Or(err, failure)
	}
	paths, failure := project(command.command, true)
	if len(paths) == 0 {
		if failure != nil {
			return pythonCorpusReason(failure.Error()), false
		}
		return "no predicted difference", false
	}
	// Feed the command as it would arrive, cut at fixed fractions.
	streamed := false
	for cut := 1; cut < 20 && !streamed; cut++ {
		partial, _ := project(command.command[:len(command.command)*cut/20], false)
		streamed = len(partial) != 0
	}
	return "", streamed
}

// pythonCorpusReason keeps the failure kind and offending node kind, with a
// short excerpt of a call's callee, so similar stops group together.
func pythonCorpusReason(reason string) string {
	kind, node, found := strings.Cut(reason, ": ")
	if !found {
		return reason
	}
	nodeKind, text, _ := strings.Cut(node, " ")
	if unquoted, err := strconv.Unquote(text); err == nil {
		text = unquoted
	}
	switch nodeKind {
	case "call", "call_expression", "expression_statement":
		if index := strings.IndexByte(text, '('); index > 0 {
			text = text[:index]
		}
		text = strings.TrimSpace(text)
		if index := strings.LastIndexAny(text, ".= "); index >= 0 {
			text = text[index:]
		}
		return kind + ": " + nodeKind + " " + text
	}
	return kind + ": " + nodeKind
}

func pythonCorpusUnescape(literal string) string {
	return strings.NewReplacer(`\n`, "\n", `\t`, "\t", `\\`, `\`, `\'`, `'`, `\"`, `"`).Replace(literal)
}

// Freeze serializes private session-derived input only on explicit opt-in. Never
// print it: the same population must be replayed across interpreter overlays.
type pythonCorpusFrozenCommand struct{ Agent, Origin, Directory, Command, Category string }
type pythonCorpusFrozen struct {
	Commands    []pythonCorpusFrozenCommand
	Populations map[string]*pythonCorpusPopulation
}

func pythonCorpusSave(path string, commands []pythonCorpusCommand, populations map[string]*pythonCorpusPopulation) error {
	frozen := pythonCorpusFrozen{Populations: populations}
	for _, c := range commands {
		frozen.Commands = append(frozen.Commands, pythonCorpusFrozenCommand{c.agent, c.origin, c.directory, c.command, c.category})
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	return gob.NewEncoder(f).Encode(frozen)
}
func pythonCorpusLoad(path string) ([]pythonCorpusCommand, map[string]*pythonCorpusPopulation, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	var frozen pythonCorpusFrozen
	if err := gob.NewDecoder(f).Decode(&frozen); err != nil {
		return nil, nil, err
	}
	var commands []pythonCorpusCommand
	for _, c := range frozen.Commands {
		commands = append(commands, pythonCorpusCommand{c.Agent, c.Origin, c.Directory, c.Command, c.Category})
	}
	return commands, frozen.Populations, nil
}
