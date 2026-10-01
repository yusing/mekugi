package toolplugin

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/yusing/mekugi/internal/golex"
	"github.com/yusing/mekugi/internal/sourcekind"
)

type symbolPosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}
type symbolRange struct {
	Start symbolPosition `json:"start"`
	End   symbolPosition `json:"end"`
}
type symbolLocation struct {
	outside        bool
	path           string
	line, from, to int
	position       *symbolRange
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
	parsed *parsedSource
}
type symbolFailure struct{ class, message string }

func (e *symbolFailure) Error() string { return e.message }

type symbolSourceFailure struct{ reason, message string }

func (e *symbolSourceFailure) Error() string { return e.message }

const symbolUsage = "msymbol [--max-tokens N] [--workspace ROOT] (def|refs) PATH [LINE] SYMBOL [N] [(def|refs) PATH [LINE] SYMBOL [N] ...]"

var symbolDigits = regexp.MustCompile(`^[0-9]+$`)
var combinedSymbolPath = regexp.MustCompile(`^(.*):([1-9][0-9]*)$`)

func positiveSymbolInteger(s, label string) (int, error) {
	n, e := strconv.Atoi(s)
	if e != nil || n > 9007199254740991 {
		return 0, fmt.Errorf("%s is too large", label)
	}
	if n < 1 || s != strconv.Itoa(n) {
		return 0, fmt.Errorf("%s must be a positive decimal integer", label)
	}
	return n, nil
}

// Source: plugins/msymbol.ts:132:176@[543de4f3] parseQueries
func parseSymbolQueries(args []string) ([]*symbolQuery, string, bool, int, error) {
	o, rest, err := nativeReaderOptions(args, false, 4000, "--workspace")
	if err != nil {
		return nil, "", false, 0, err
	}
	var operands []string
	workspace := ""
	explicit, ended := false, false
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		if a == "--" && !ended {
			ended = true
			continue
		}
		if a == "--workspace" && !ended {
			if explicit || i+1 >= len(rest) || rest[i+1] == "" || strings.ContainsRune(rest[i+1], 0) {
				return nil, "", false, 0, errors.New("--workspace requires one usable directory")
			}
			explicit = true
			i++
			workspace = rest[i]
			continue
		}
		operands = append(operands, a)
	}
	var queries []*symbolQuery
	for i := 0; i < len(operands); {
		q := &symbolQuery{mode: operands[i]}
		i++
		if q.mode != "def" && q.mode != "refs" {
			return nil, "", false, 0, errors.New("mode must be def or refs")
		}
		if i >= len(operands) || operands[i] == "" || strings.ContainsRune(operands[i], 0) {
			return nil, "", false, 0, errors.New("path must be usable")
		}
		q.path = operands[i]
		i++
		if match := combinedSymbolPath.FindStringSubmatch(q.path); match != nil {
			q.path = match[1]
			if strings.HasPrefix(q.path, "\"") {
				if json.Unmarshal([]byte(q.path), &q.path) != nil {
					return nil, "", false, 0, errors.New("invalid quoted PATH:LINE")
				}
			}
			q.line, err = positiveSymbolInteger(match[2], "line")
		} else if i < len(operands) && symbolDigits.MatchString(operands[i]) {
			q.line, err = positiveSymbolInteger(operands[i], "line")
			i++
		}
		if err != nil {
			return nil, "", false, 0, err
		}
		if i >= len(operands) {
			return nil, "", false, 0, fmt.Errorf("usage: %s", symbolUsage)
		}
		q.name = operands[i]
		i++
		if _, last, ok := strings.CutLast(q.name, "."); ok {
			q.name = last
		}
		if q.name == "" {
			return nil, "", false, 0, errors.New("SYMBOL must end with a usable name")
		}
		if i < len(operands) && symbolDigits.MatchString(operands[i]) {
			q.occurrence, err = positiveSymbolInteger(operands[i], "N")
			i++
			if err != nil {
				return nil, "", false, 0, err
			}
			if q.line == 0 {
				return nil, "", false, 0, errors.New("N requires an explicit LINE")
			}
		}
		queries = append(queries, q)
	}
	if len(queries) == 0 {
		return nil, "", false, 0, fmt.Errorf("usage: %s", symbolUsage)
	}
	return queries, workspace, explicit, o.budget, nil
}
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
	parsed, err := parseNativeSource(ctx, string(data), format)
	if err != nil {
		return nil, err
	}
	file := &symbolSource{canonical, parsed}
	cache[canonical] = file
	return file, nil
}
func nearbySymbolLines(s *parsedSource, name string, line int) string {
	var lines []int
	seen := map[int]bool{}
	for _, t := range s.tokens {
		if t.text == name {
			n := s.lines.at(t.from)
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
func selectNativeSymbol(q *symbolQuery) error {
	s := q.file.parsed
	if q.line == 0 {
		var entries []outlineEntry
		for _, e := range s.entries {
			if e.kind != "parse_error" && e.name == q.name && e.nameFrom >= 0 {
				entries = append(entries, e)
			}
		}
		if len(entries) > 1 {
			var lines []string
			for _, e := range entries {
				lines = append(lines, strconv.Itoa(s.lines.at(e.nameFrom)))
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
		if !entries[0].complete {
			return fmt.Errorf("%s has an incomplete outline declaration; supply LINE", q.name)
		}
		q.offset = entries[0].nameFrom
		q.line = s.lines.at(q.offset)
		return nil
	}
	if _, ok := s.lines.row(q.line); !ok {
		return fmt.Errorf("line %d is past EOF", q.line)
	}
	if s.format.Language == "go" && !golex.IsIdentifier(q.name) {
		return errors.New("SYMBOL must be a non-keyword Go identifier")
	}
	var offsets []int
	for _, t := range s.tokens {
		if t.text == q.name && s.lines.at(t.from) == q.line {
			offsets = append(offsets, t.from)
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
func nativeSymbol(ctx context.Context, args []string) (ExecutionOutput, error) {
	queries, root, explicit, budget, err := parseSymbolQueries(args)
	if err != nil {
		return nativeFailure("msymbol", err, "invalid_arguments"), nil
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
		return nativeFailure("msymbol", err, "invalid_source"), nil
	}
	cache := map[string]*symbolSource{}
	groups := map[string][]*symbolQuery{}
	for _, q := range queries {
		q.file, q.err = loadSymbolSource(ctx, root, q.path, "", cache)
		if q.err == nil {
			q.err = selectNativeSymbol(q)
		}
		if q.err == nil {
			resolver := q.file.parsed.format.SemanticResolver
			groups[resolver] = append(groups[resolver], q)
		}
	}
	var wg sync.WaitGroup
	for resolver, group := range groups {
		wg.Go(func() {
			if resolver == "gopls" && len(group) == 1 {
				group[0].locations, group[0].stderr, group[0].err = runNativeGopls(ctx, root, group[0])
			} else {
				runNativeLSP(ctx, root, resolver, group)
			}
		})
	}
	wg.Wait()
	if enabled, _ := ctx.Value(frontendOrphanCleanupKey{}).(bool); enabled {
		cleanupFrontendOrphans()
	}
	if err := ctx.Err(); err != nil {
		return ExecutionOutput{}, err
	}
	// Never emit a semantic result for an input that changed during resolution.
	for _, q := range queries {
		if q.file == nil || q.err != nil {
			continue
		}
		path, e := filepath.EvalSymlinks(q.file.path)
		data, readErr := os.ReadFile(q.file.path)
		if e != nil || readErr != nil || path != q.file.path || string(data) != q.file.parsed.source {
			q.err = errors.New("input changed during query")
		}
	}
	clear(cache)
	result := ExecutionOutput{}
	counter := nativeBudget{}
	var shown, retained strings.Builder
	incomplete, retentionFailed := false, false
	appendRow := func(row string) error {
		if retentionFailed {
			return &symbolFailure{"output_limit", "complete reference output exceeds the 16 MiB retention bound"}
		}
		if retained.Len()+len(row) > nativeRetainedBytes {
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
			label = fmt.Sprintf("%s %s %s %s: ", q.mode, nativeJSON(q.path), line, q.name)
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
			file, e := loadSymbolSource(ctx, root, loc.path, q.file.parsed.format.SemanticResolver, cache)
			if e != nil {
				if f, ok := errors.AsType[*symbolSourceFailure](e); ok {
					skip(f.reason)
					continue
				}
				queryErr = e
				break
			}
			start, end := loc.line, loc.line
			from, to := loc.from, loc.to
			if loc.position != nil {
				var ok1, ok2 bool
				from, ok1 = file.parsed.lines.offset(loc.position.Start)
				to, ok2 = file.parsed.lines.offset(loc.position.End)
				if !ok1 || !ok2 || to < from {
					skip("unavailable")
					continue
				}
				start = loc.position.Start.Line + 1
				end = start
			}
			if q.mode == "def" && from >= 0 {
				for _, entry := range file.parsed.entries {
					if entry.kind != "import" && entry.complete && entry.nameFrom == from && entry.nameTo == to {
						start, end = entry.line, entry.endLine
						break
					}
				}
			}
			headerEnd := start - 1
			for line := start; line <= end; line++ {
				row, ok := file.parsed.lines.row(line)
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
				label = nativeJSON(filepath.ToSlash(label))
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
					if referenceBytes > nativeRetainedBytes {
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
	result.Stdout, err = counter.selectRows(shown.String(), budget, false)
	if err != nil {
		return ExecutionOutput{}, err
	}
	incomplete = incomplete || len(result.Stdout) < shown.Len()
	if incomplete {
		result.ExitCode = 1
		result.FailureClass = "output_limit"
		result.Stderr += fmt.Sprintf("msymbol: output incomplete: %d-token limit reached\n", budget)
		if !retentionFailed {
			result.OmittedOutput = &OmittedOutput{Stdout: strings.TrimPrefix(retained.String(), result.Stdout), StdoutKind: "rows"}
		}
	}
	return result, nil
}
