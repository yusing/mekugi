package query

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/yusing/mekugi/libraries/sourceanalysis/structure"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/libraries/commandargs"
	"github.com/yusing/mekugi/libraries/outputbudget"

	"go/token"

	"github.com/yusing/mekugi/libraries/sourceanalysis/sourcekind"
)

type symbolRange struct {
	Start structure.Position `json:"start"`
	End   structure.Position `json:"end"`
}
type symbolLocation struct {
	outside  bool
	path     string
	position *symbolRange
}
type symbolQuery struct {
	mode, path, name         string
	line, occurrence, offset int
	file                     *symbolSource
	err                      error
	locations                []symbolLocation
	stderr                   string
}
type symbolSource struct {
	path   string
	parsed *structure.Source
}
type symbolFailure struct{ class, message string }

func (e *symbolFailure) Error() string { return e.message }

type symbolSourceFailure struct{ reason, message string }

func (e *symbolSourceFailure) Error() string { return e.message }

func outsideSymbolWorkspace(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative)
}
func loadSymbolSource(ctx context.Context, root, path, resolver string, cache map[string]*symbolSource) (*symbolSource, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	path = filepath.Clean(path)
	if outsideSymbolWorkspace(root, path) {
		return nil, &symbolSourceFailure{"outside workspace", "path is outside the workspace"}
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, &symbolSourceFailure{"unavailable", "cannot read path: " + err.Error()}
	}
	if outsideSymbolWorkspace(root, canonical) {
		return nil, &symbolSourceFailure{"outside workspace", "path resolves outside the workspace"}
	}
	format, ok := sourcekind.Classify(canonical)
	if !ok || format.SemanticResolver == "" || resolver != "" && format.SemanticResolver != resolver {
		reason := "not TypeScript"
		if resolver == "gopls" {
			reason = "not Go"
		}
		if resolver == "python" {
			reason = "not Python"
		}
		return nil, &symbolSourceFailure{reason, "path has an unsupported msymbol source format"}
	}
	if prior := cache[canonical]; prior != nil {
		return prior, nil
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return nil, &symbolSourceFailure{"unavailable", err.Error()}
	}
	if !info.Mode().IsRegular() {
		return nil, &symbolSourceFailure{"not regular", "path is not a regular file"}
	}
	data, err := os.ReadFile(canonical)
	if err != nil {
		return nil, &symbolSourceFailure{"unavailable", err.Error()}
	}
	if !utf8.Valid(data) {
		return nil, &symbolSourceFailure{"not UTF-8", "path is not UTF-8"}
	}
	parsed, err := structure.Parse(ctx, string(data), format)
	if err != nil {
		return nil, err
	}
	file := &symbolSource{canonical, parsed}
	cache[canonical] = file
	return file, nil
}
func nearbySymbolLines(s *structure.Source, name string, line int) string {
	var lines []int
	seen := map[int]bool{}
	for _, t := range s.Tokens {
		if t.Text == name {
			n := s.Lines.At(t.From)
			if !seen[n] {
				lines = append(lines, n)
				seen[n] = true
			}
		}
	}
	if line > 0 {
		slices.SortStableFunc(lines, func(a, b int) int { return absSymbol(a-line) - absSymbol(b-line) })
	}
	lines = lines[:min(5, len(lines))]
	slices.Sort(lines)
	values := make([]string, len(lines))
	for i, n := range lines {
		values[i] = strconv.Itoa(n)
	}
	return strings.Join(values, ", ")
}
func absSymbol(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
func selectSymbol(q *symbolQuery) error {
	s := q.file.parsed
	if q.line == 0 {
		var entries []structure.Entry
		for _, e := range s.Entries {
			if e.Kind != "parse_error" && e.Name == q.name && e.NameFrom >= 0 {
				entries = append(entries, e)
			}
		}
		if len(entries) > 1 {
			var lines []string
			for _, e := range entries {
				lines = append(lines, strconv.Itoa(s.Lines.At(e.NameFrom)))
			}
			return fmt.Errorf("%s has %d outline matches (lines %s); supply LINE", q.name, len(entries), strings.Join(lines, ", "))
		}
		if len(entries) == 0 {
			hint := "no matching token in this file"
			if lines := nearbySymbolLines(s, q.name, 0); lines != "" {
				hint = "supply LINE, such as a token line: " + lines
			}
			return fmt.Errorf("%s has no outline declaration; %s", q.name, hint)
		}
		if !entries[0].Complete {
			return fmt.Errorf("%s has an incomplete outline declaration; supply LINE", q.name)
		}
		q.offset = entries[0].NameFrom
		q.line = s.Lines.At(q.offset)
		return nil
	}
	if _, ok := s.Lines.Row(q.line); !ok {
		return fmt.Errorf("line %d is past EOF", q.line)
	}
	if s.Format.Language == "go" && !token.IsIdentifier(q.name) {
		return errors.New("SYMBOL must be a non-keyword Go identifier")
	}
	var offsets []int
	for _, t := range s.Tokens {
		if t.Text == q.name && s.Lines.At(t.From) == q.line {
			offsets = append(offsets, t.From)
		}
	}
	if len(offsets) == 0 {
		hint := "no matching token in this file"
		if lines := nearbySymbolLines(s, q.name, q.line); lines != "" {
			hint = "nearby lines: " + lines
		}
		return fmt.Errorf("%s is not a symbol token on the selected line; %s", q.name, hint)
	}
	if q.occurrence == 0 {
		if len(offsets) > 1 {
			return fmt.Errorf("%s is ambiguous on the selected line (%d occurrences); supply N", q.name, len(offsets))
		}
		q.offset = offsets[0]
	} else {
		if q.occurrence > len(offsets) {
			return fmt.Errorf("symbol occurrence %d is missing", q.occurrence)
		}
		q.offset = offsets[q.occurrence-1]
	}
	return nil
}
func symbolFileURI(path string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
}
func symbolURIPath(uri string) (string, error) {
	u, e := url.Parse(uri)
	if e != nil || u.Scheme != "file" || u.Host != "" && u.Host != "localhost" || u.RawQuery != "" || u.Fragment != "" {
		return "", &symbolSourceFailure{"outside workspace", "location is not a workspace file"}
	}
	return filepath.FromSlash(u.Path), nil
}

// Execute runs one command-local set of semantic queries.
func (Service) Execute(ctx context.Context, request command.Request) (command.Output, error) {
	args := request.Args
	if err := ctx.Err(); err != nil {
		return command.Output{}, err
	}
	dedicated := request.ProcessScope == command.DedicatedHostProcess
	if dedicated {
		if err := enableFrontendSubreaper(); err != nil {
			return command.Output{}, fmt.Errorf("prepare frontend process cleanup: %w", err)
		}
	}

	arguments, err := commandargs.ParseSymbolQueries(args)
	if err != nil {
		return failure(err, "invalid_arguments"), nil
	}
	root, explicit, budget := arguments.Workspace, arguments.ExplicitWorkspace, arguments.Budget
	queries := make([]*symbolQuery, len(arguments.Queries))
	for i, selection := range arguments.Queries {
		queries[i] = &symbolQuery{mode: selection.Mode, path: selection.Path, name: selection.Name, line: selection.Line, occurrence: selection.Occurrence}
	}
	if root == "" {
		root, err = os.Getwd()
	}
	if err == nil {
		root, err = filepath.Abs(root)
	}
	if err == nil {
		root, err = filepath.EvalSymlinks(root)
	}
	if err == nil {
		var info os.FileInfo
		info, err = os.Stat(root)
		if err == nil && !info.IsDir() {
			err = errors.New("workspace is not a directory")
		}
	}
	if err != nil {
		return failure(err, "invalid_source"), nil
	}
	cache := map[string]*symbolSource{}
	groups := map[string][]*symbolQuery{}
	for _, q := range queries {
		q.file, q.err = loadSymbolSource(ctx, root, q.path, "", cache)
		if q.err == nil {
			q.err = selectSymbol(q)
		}
		if q.err == nil {
			resolver := q.file.parsed.Format.SemanticResolver
			groups[resolver] = append(groups[resolver], q)
		}
	}
	var scratch string
	if len(groups) != 0 {
		scratch, err = os.MkdirTemp("", "mekugi-symbol-")
		if err != nil {
			return failure(fmt.Errorf("create resolver scratch: %w", err), "resolver_error"), nil
		}
	}
	var wg sync.WaitGroup
	for resolver, group := range groups {
		wg.Go(func() {
			runLSP(ctx, request.ProcessScope, root, resolver, scratch, group)
		})
	}
	wg.Wait()
	if dedicated {
		cleanupFrontendOrphans()
	}
	if scratch != "" {
		if err := os.RemoveAll(scratch); err != nil {
			queries[0].stderr += fmt.Sprintf("msymbol: resolver scratch cleanup: %v\n", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return command.Output{}, err
	}
	// Never emit a semantic result for an input that changed during resolution.
	for _, q := range queries {
		if q.file == nil || q.err != nil {
			continue
		}
		path, e := filepath.EvalSymlinks(q.file.path)
		data, readErr := os.ReadFile(q.file.path)
		if e != nil || readErr != nil || path != q.file.path || string(data) != q.file.parsed.Source {
			q.err = errors.New("input changed during query")
		}
	}
	clear(cache)
	result := command.Output{}
	counter := outputbudget.Budget{}
	var shown, retained strings.Builder
	incomplete, retentionFailed := false, false
	appendRow := func(row string) error {
		if retentionFailed {
			return &symbolFailure{"output_limit", "complete reference output exceeds the 16 MiB retention bound"}
		}
		if retained.Len()+len(row) > retainedBytes {
			retentionFailed = true
			return &symbolFailure{"output_limit", "complete reference output exceeds the 16 MiB retention bound"}
		}
		retained.WriteString(row)
		if !incomplete {
			if shown.Len()+len(row) <= budget*128 {
				shown.WriteString(row)
			} else {
				incomplete = true
			}
		}
		return nil
	}
	fail := func(q *symbolQuery, e error) {
		class := "resolver_error"
		if f, ok := errors.AsType[*symbolFailure](e); ok {
			class = f.class
		}
		if _, ok := errors.AsType[*symbolSourceFailure](e); ok {
			class = "invalid_source"
		}
		label := ""
		if len(queries) > 1 {
			line := ""
			if q.line > 0 {
				line = strconv.Itoa(q.line)
			}
			label = fmt.Sprintf("%s %s %s %s: ", q.mode, quotePath(q.path), line, q.name)
		}
		result.Stderr += "msymbol: " + label + e.Error() + "\n"
		result.ExitCode = 1
		if result.FailureClass == "" {
			result.FailureClass = class
		}
	}
	skipped := map[string]int{}
	var skipOrder []string
	skip := func(reason string) {
		if skipped[reason] == 0 {
			skipOrder = append(skipOrder, reason)
		}
		skipped[reason]++
	}
	for _, q := range queries {
		if q.err != nil {
			fail(q, q.err)
			continue
		}
		result.Stderr += q.stderr
		if result.Stderr != "" && !strings.HasSuffix(result.Stderr, "\n") {
			result.Stderr += "\n"
		}
		seen := map[string]bool{}
		references := map[string][]string{}
		var referenceOrder []string
		referenceBytes := 0
		emitted := false
		var queryErr error
		for _, loc := range q.locations {
			if loc.outside {
				skip("outside workspace")
				continue
			}
			file, e := loadSymbolSource(ctx, root, loc.path, q.file.parsed.Format.SemanticResolver, cache)
			if e != nil {
				if f, ok := errors.AsType[*symbolSourceFailure](e); ok {
					skip(f.reason)
					continue
				}
				queryErr = e
				break
			}
			from, ok1 := file.parsed.Lines.Offset(loc.position.Start)
			to, ok2 := file.parsed.Lines.Offset(loc.position.End)
			if !ok1 || !ok2 || to < from {
				skip("unavailable")
				continue
			}
			start := loc.position.Start.Line + 1
			end := start
			if q.mode == "def" {
				for _, entry := range file.parsed.Entries {
					if entry.Kind != "import" && entry.Complete && entry.NameFrom == from && entry.NameTo == to {
						start, end = entry.Line, entry.EndLine
						break
					}
				}
			}
			headerEnd := start - 1
			for line := start; line <= end; line++ {
				row, ok := file.parsed.Lines.Row(line)
				if !ok {
					skip("unavailable")
					break
				}
				key := file.path + "\x00" + strconv.Itoa(line)
				if seen[key] {
					continue
				}
				seen[key] = true
				label := file.path
				if !explicit {
					label, _ = filepath.Rel(root, file.path)
				}
				label = quotePath(filepath.ToSlash(label))
				if q.mode == "def" {
					if line > headerEnd {
						headerEnd = line
						for headerEnd < end && !seen[file.path+"\x00"+strconv.Itoa(headerEnd+1)] {
							headerEnd++
						}
						if queryErr = appendRow(fmt.Sprintf("%s:%d-%d\n", label, line, headerEnd)); queryErr != nil {
							break
						}
					}
					queryErr = appendRow(row + "\n")
				} else {
					formatted := fmt.Sprintf("%d %s\n", line, row)
					referenceBytes += len(formatted)
					if referenceBytes > retainedBytes {
						queryErr = &symbolFailure{"output_limit", "complete reference output exceeds the 16 MiB retention bound"}
						break
					}
					if _, ok := references[label]; !ok {
						referenceOrder = append(referenceOrder, label)
					}
					references[label] = append(references[label], formatted)
				}
				emitted = true
				if queryErr != nil {
					break
				}
			}
			if queryErr != nil {
				break
			}
		}
		if queryErr == nil {
			for _, label := range referenceOrder {
				if queryErr = appendRow(label + ":\n"); queryErr != nil {
					break
				}
				for _, row := range references[label] {
					if queryErr = appendRow(row); queryErr != nil {
						break
					}
				}
				if queryErr != nil {
					break
				}
			}
		}
		if queryErr != nil {
			fail(q, queryErr)
		} else if q.mode == "def" && !emitted {
			fail(q, &symbolFailure{"no_editable_location", "definition has no editable workspace location"})
		}
	}
	if len(skipOrder) > 0 {
		parts := []string{}
		for _, reason := range skipOrder {
			noun := "locations"
			if skipped[reason] == 1 {
				noun = "location"
			}
			parts = append(parts, fmt.Sprintf("%d %s %s", skipped[reason], noun, reason))
		}
		result.Stderr += "msymbol: skipped " + strings.Join(parts, ", ") + "\n"
	}
	result.Stdout, err = counter.SelectRows(shown.String(), budget, false)
	if err != nil {
		return command.Output{}, err
	}
	incomplete = incomplete || len(result.Stdout) < shown.Len()
	if incomplete {
		result.ExitCode = 1
		result.FailureClass = "output_limit"
		result.Stderr += fmt.Sprintf("msymbol: output incomplete: %d-token limit reached\n", budget)
		if !retentionFailed {
			result.OmittedOutput = &command.OmittedOutput{Stdout: strings.TrimPrefix(retained.String(), result.Stdout), StdoutKind: "rows"}
		}
	}
	return result, nil
}
