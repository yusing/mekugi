package router

import (
	"cmp"
	"path/filepath"
	"strconv"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

type appServerWebSearchAction struct {
	Type    string   `json:"type"`
	Query   string   `json:"query"`
	Queries []string `json:"queries"`
	URL     string   `json:"url"`
	Pattern string   `json:"pattern"`
}

func appServerToolText(item appServerItem, cwd string) string {
	if item.Type != "webSearch" {
		return appServerCommandText(item, cwd)
	}
	query := item.Query
	if action := item.Action; action != nil {
		switch action.Type {
		case "openPage":
			return "Open " + commentaryCode(action.URL)
		case "findInPage":
			return "Search " + commentaryCode(action.Pattern) + " in " + commentaryCode(action.URL)
		case "search":
			query = cmp.Or(strings.Join(action.Queries, " | "), action.Query, query)
		}
	}
	return "Search " + commentaryCode(query)
}

// Count returned records only when host evidence is complete and attributable
// to one search. Missing output, errors, context rows and truncated output are
// not zero results. No command is executed to infer a count.
func appServerSearchResults(item appServerItem) *int {
	if item.Type == "webSearch" {
		if item.Results != nil {
			return new(len(item.Results))
		}
		return nil
	}
	if item.Type != "commandExecution" || item.Status != "completed" || item.ExitCode == nil || *item.ExitCode < 0 || *item.ExitCode > 1 {
		return nil
	}
	source := appServerDisplayCommand(item.Command)
	program, err := syntax.NewParser().Parse(strings.NewReader(source), "")
	if err != nil || len(program.Stmts) != 1 {
		return nil
	}
	argv, ok := toolActivityPatternCall(source, program.Stmts[0])
	if !ok || len(argv) < 2 {
		return nil
	}
	argv[0] = filepath.Base(argv[0])
	if argv[0] != "rg" && argv[0] != "grep" {
		return nil
	}
	display, ok := toolActivitySearch(argv, "")
	if !ok || !strings.HasPrefix(display, "Search ") {
		return nil
	}
	counts, files := false, false
	for i := 1; i < len(argv); i++ {
		arg := argv[i]
		if arg == "--" {
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			continue
		}
		for arg != "" {
			flag, _, attached := strings.Cut(arg, "=")
			rest := ""
			if !strings.HasPrefix(arg, "--") {
				flag, rest = arg[:2], arg[2:]
				attached = rest != ""
			}
			switch flag {
			case "-A", "-B", "-C", "-q", "-U", "-z", "-Z", "--after-context", "--before-context", "--context", "--quiet", "--multiline", "--null", "--null-data", "--json", "--stats", "--heading", "--replace", "--pre", "-M", "--max-columns":
				return nil
			case "-r":
				if argv[0] == "rg" {
					return nil
				}
			case "-l", "-L", "--files-with-matches", "--files-without-match":
				files = true
			case "-c", "--count", "--count-matches":
				counts = true
			}
			if toolActivitySearchFlag(argv[0], flag) > 0 {
				if !attached {
					i++
				}
				break
			}
			arg = ""
			if rest != "" {
				arg = "-" + rest
			}
		}
	}
	if counts && files {
		return nil
	}
	if item.AggregatedOutput == nil {
		if *item.ExitCode == 1 {
			return new(0)
		}
		return nil
	}
	output := *item.AggregatedOutput
	// Codex's shell aggregate can silently cap at 1 MiB.
	// Source: codex-rs/core/src/exec.rs:865:910 (DEFAULT_OUTPUT_BYTES_CAP).
	if len(output) >= 1<<20 || strings.Contains(strings.ToLower(output), "omitted") {
		return nil
	}
	if strings.ContainsAny(output, "\x00\x1b") || strings.Contains(strings.ToLower(output), "truncat") || strings.Contains(output, "binary file") || strings.Contains(output, "Binary file") {
		return nil
	}
	if output == "" {
		if *item.ExitCode == 1 {
			return new(0)
		}
		return nil
	}
	rows := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	if !counts {
		return new(len(rows))
	}
	total := 0
	for _, row := range rows {
		_, count, found := strings.CutLast(row, ":")
		if !found {
			count = row
		}
		n, err := strconv.Atoi(strings.TrimSuffix(count, "\r"))
		if err != nil || n < 0 || total > int(^uint(0)>>1)-n {
			return nil
		}
		total += n
	}
	return new(total)
}
