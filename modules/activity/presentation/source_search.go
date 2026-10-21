package presentation

// Source: apps/mekugi/internal/router/subagent_tool_display_search.go:1:125@0a356401.

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"slices"
	"strconv"
	"strings"

	"github.com/yusing/mekugi/libraries/commandanalysis/shellsyntax"
	"mvdan.cc/sh/v3/syntax"

	"github.com/yusing/mekugi/libraries/pathdisplay"
)

// Source: codex-rs/shell-command/src/parse_command.rs:1787:1842,2349:2389
// and codex-rs/tui/src/exec_cell/render.rs:384:392@6f6af0fce4381233fa2e83dc24641167e68dfcb5.
// Display query and target instead of execution flags. Shorten paths against
// the known command directory and preserve all explicit patterns; this
// projection never changes the shell command.
func sourceSearch(argv []string, cwd string) (string, bool) {
	var queries, paths []string
	files, explicit, optionsEnded := false, false, false
	for i := 1; i < len(argv); i++ {
		arg := argv[i]
		if !optionsEnded && arg == "--" {
			optionsEnded = true
			continue
		}
		if !optionsEnded && strings.HasPrefix(arg, "-") && arg != "-" {
			for arg != "" {
				flag, value, attached := strings.Cut(arg, "=")
				remainder := ""
				if !strings.HasPrefix(arg, "--") {
					flag, remainder = arg[:2], arg[2:]
					value, attached = remainder, remainder != ""
				}
				kind := sourceSearchFlag(argv[0], flag)
				if kind < 0 {
					return "", false
				}
				if kind == 0 || kind == 3 {
					files = files || kind == 3
					arg = ""
					if remainder != "" {
						arg = "-" + remainder
					}
					continue
				}
				if !attached {
					if i+1 == len(argv) {
						return "", false
					}
					i++
					value = argv[i]
				}
				if kind == 2 {
					explicit = true
					queries = append(queries, value)
				}
				arg = ""
			}
			continue
		}
		paths = append(paths, arg)
	}
	if files {
		if len(paths) == 0 {
			paths = []string{"."}
		}
		for i, path := range paths {
			paths[i] = "List " + SourceCode(pathdisplay.ForWorkspace(cwd, path))
		}
		return strings.Join(paths, "\n\n"), true
	}
	if !explicit && len(paths) != 0 {
		queries, paths = paths[:1], paths[1:]
	}
	if len(queries) == 0 {
		return "", false
	}
	var display strings.Builder
	display.WriteString("Search " + SourceCode(strings.Join(queries, " | ")))
	if len(paths) != 0 {
		for i, path := range paths {
			if i == 0 {
				display.WriteString(" in ")
			} else {
				display.WriteString(" ")
			}
			display.WriteString(SourceCode(pathdisplay.ForWorkspace(cwd, path)))
		}
	}
	return display.String(), true
}

// 0: switch, 1: option value, 2: pattern value, 3: file listing.
// Unknown arity keeps the original Run preview rather than guessing a query.
func sourceSearchFlag(command, flag string) int {
	switch flag {
	case "--files":
		if command == "rg" {
			return 3
		}
	case "-E", "-r", "-T", "--color", "--colour":
		if command == "rg" {
			return 1
		}
		return 0
	case "-e", "--regexp", "-f", "--file":
		return 2
	case "-g", "--glob", "--iglob", "-t", "--type", "--type-not", "--type-add", "--type-clear",
		"-m", "--max-count", "-A", "--after-context", "-B", "--before-context", "-C", "--context",
		"--max-depth", "--max-filesize", "--encoding", "--engine", "--threads", "-j",
		"--sort", "--sortr", "--colors", "--path-separator", "--context-separator",
		"--include", "--exclude", "--exclude-dir", "--exclude-from", "--label", "-d", "--directories", "-D", "--devices",
		"--replace", "--pre", "--pre-glob", "--ignore-file", "--binary-files", "-M", "--max-columns":
		return 1
	case "-n", "-N", "-F", "-G", "-P", "-i", "-s", "-S", "-w", "-x", "-v", "-l", "-L", "-c", "-o", "-q",
		"-a", "-b", "-H", "-h", "-I", "-U", "-u", "-z", "-Z", "-R",
		"--line-number", "--no-line-number", "--fixed-strings", "--ignore-case", "--smart-case", "--case-sensitive",
		"--word-regexp", "--line-regexp", "--invert-match", "--files-with-matches", "--files-without-match",
		"--count", "--count-matches", "--only-matching", "--quiet", "--text", "--byte-offset", "--with-filename",
		"--no-filename", "--hidden", "--no-ignore", "--no-ignore-vcs", "--no-ignore-parent", "--no-ignore-global",
		"--no-messages", "--no-heading", "--heading", "--no-config", "--follow", "--pcre2", "--multiline",
		"--multiline-dotall", "--null", "--null-data", "--json", "--stats", "--trim", "--vimgrep", "--column",
		"--recursive", "--dereference-recursive", "--extended-regexp", "--basic-regexp", "--perl-regexp":
		return 0
	}
	return -1
}

// OperationResultCount interprets complete output for one search, listing or inspection.
// The adapter establishes host completion and applies host-specific output caps;
// unknown or incomplete output never becomes a zero count.
func OperationResultCount(source string, output *string, exit int) *int {
	if exit < 0 || exit > 1 {
		return nil
	}
	program, err := syntax.NewParser().Parse(strings.NewReader(source), "")
	if err != nil || len(program.Stmts) != 1 {
		return nil
	}
	argv, ok := shellsyntax.PatternCall(source, program.Stmts[0])
	if !ok || len(argv) < 1 {
		return nil
	}
	if output != nil && (len(*output) >= 1<<20 || strings.ContainsAny(*output, "\x00\x1b")) {
		return nil
	}
	switch argv[0] {
	case "ls":
		if exit != 0 || output == nil || strings.Contains(strings.ToLower(*output), "omitted") || strings.Contains(strings.ToLower(*output), "truncat") {
			return nil
		}
		// A syntactic operand can expand into several directory operands.
		// Only literal arguments establish the listing's output shape.
		if _, literal := shellsyntax.PatternCall("", program.Stmts[0]); !literal {
			return nil
		}
		operands, ok := sourceListOperands(argv)
		if !ok || len(operands) > 1 {
			return nil // Multiple directories add ambiguous headings.
		}
		long, sized := false, false
		for i, arg := range argv[1:] {
			if slices.Contains(operands, i+1) || arg == "--" {
				continue
			}
			if !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "--") || strings.Trim(arg[1:], "1lngaAdoFhipqQrstuvX") != "" || strings.Contains(arg, "R") {
				return nil
			}
			long = long || strings.ContainsAny(arg, "lnog")
			sized = sized || strings.Contains(arg, "s")
		}
		rows := strings.Split(strings.TrimSuffix(*output, "\n"), "\n")
		if *output == "" {
			return new(0)
		}
		if !long && (strings.Contains(*output, "\t") || strings.Contains(*output, "  ")) {
			return nil // A terminal listing can place several entries on one row.
		}
		if (long || sized) && strings.HasPrefix(rows[0], "total ") {
			if _, err := strconv.ParseUint(strings.TrimPrefix(rows[0], "total "), 10, 64); err != nil {
				return nil
			}
			rows = rows[1:]
		}
		for _, row := range rows {
			if row == "" || strings.HasSuffix(row, ":") {
				return nil // Expanded directory lists contain headers and separators.
			}
		}
		return new(len(rows))
	case "inspect_file":
		if exit != 0 || output == nil {
			return nil
		}
		count := 0
		for row := range strings.SplitSeq(strings.TrimSuffix(*output, "\n"), "\n") {
			if row == "" || row == "(no outline)" || strings.HasPrefix(row, "--- ") && strings.HasSuffix(row, " ---") {
				continue
			}
			if strings.HasPrefix(row, "{") {
				var envelope struct {
					OK        bool `json:"ok"`
					Truncated bool `json:"truncated"`
					Data      struct {
						Outline []jsontext.Value `json:"outline"`
					} `json:"data"`
				}
				if json.Unmarshal([]byte(row), &envelope) != nil || !envelope.OK || envelope.Truncated || envelope.Data.Outline == nil {
					return nil
				}
				count += len(envelope.Data.Outline)
				continue
			}
			span, rest, ok := strings.Cut(row, " ")
			start, end, ranged := strings.Cut(span, "-")
			a, errA := strconv.Atoi(start)
			b, errB := strconv.Atoi(end)
			if !ok || !ranged || errA != nil || errB != nil || a < 1 || b < a || rest == "" {
				return nil
			}
			count++
		}
		return new(count)
	case "rg", "grep":
	default:
		return nil
	}
	display, ok := sourceSearch(argv, "")
	if !ok || (!strings.HasPrefix(display, "Search ") && !strings.HasPrefix(display, "List ")) {
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
			if kind := sourceSearchFlag(argv[0], flag); kind == 1 || kind == 2 {
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
	if output == nil {
		if exit == 1 {
			return new(0)
		}
		return nil
	}
	text := *output
	if strings.Contains(strings.ToLower(text), "omitted") || strings.ContainsAny(text, "\x00\x1b") || strings.Contains(strings.ToLower(text), "truncat") || strings.Contains(text, "binary file") || strings.Contains(text, "Binary file") {
		return nil
	}
	if text == "" {
		if exit == 1 || strings.HasPrefix(display, "List ") {
			return new(0)
		}
		return nil
	}
	rows := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
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
