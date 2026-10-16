package presentation

// Source: apps/mekugi/internal/router/subagent_tool_display_shell.go:615:853@0a356401.

import (
	"cmp"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/yusing/mekugi/libraries/commandanalysis/shellsyntax"
	"github.com/yusing/mekugi/libraries/commandargs"
	"github.com/yusing/mekugi/libraries/pathdisplay"
	"mvdan.cc/sh/v3/syntax"
)

func sourceReadCommand(script string, call *syntax.CallExpr, cwd string) (string, bool) {
	var operations []struct{ label, detail string }
	add := func(label, detail string) {
		operations = append(operations, struct{ label, detail string }{label, detail})
	}
	command, literal := shellsyntax.Literal(call.Args[0])
	if !literal {
		return "", false
	}
	command = shellsyntax.CommandName(command)
	patterns := command == "rg" || command == "grep" || command == "ls" ||
		command == "cat" || command == "mcat" || command == "sed" || command == "nl" || command == "inspect_file"
	var argv []string
	for i, arg := range call.Args {
		value, literal := shellsyntax.Literal(arg)
		if !literal {
			// Classify sed by its literal flags and print program, not by
			// how the input path is constructed. Keep dynamic operands
			// verbatim so substitutions and their effects remain visible.
			if command == "sed" {
				if i != 3 {
					return "", false
				}
			} else if !patterns || !shellsyntax.PatternWord(arg) {
				return "", false
			}
			value = script[int(arg.Pos().Offset()):int(arg.End().Offset())]
		}
		argv = append(argv, value)
	}
	argv[0] = command
	switch argv[0] {
	case "mread":
		return "", true
	case "cat":
		if len(argv) < 2 {
			return "", false
		}
		for _, path := range argv[1:] {
			if path == "" || strings.HasPrefix(path, "-") {
				return "", false
			}
			label, value := "Read", pathdisplay.ForWorkspace(cwd, path)
			if filepath.Base(path) == "SKILL.md" && filepath.Dir(path) != "." {
				label, value = "Skill", filepath.Base(filepath.Dir(path))
			}
			add(label, value)
		}
	case "mcat":
		specs, _, err := commandargs.ParseReadBundle(argv[1:])
		if err != nil {
			return "", false
		}
		for _, spec := range specs {
			label, value := "Read", pathdisplay.ForWorkspace(cwd, spec.Path)
			if filepath.Base(spec.Path) == "SKILL.md" && filepath.Dir(spec.Path) != "." {
				label, value = "Skill", filepath.Base(filepath.Dir(spec.Path))
			}
			if spec.Range != "" {
				value += " " + spec.Range
			}
			add(label, value)
		}
	case "nl":
		if len(argv) != 3 || argv[1] != "-ba" || argv[2] == "" || strings.HasPrefix(argv[2], "-") {
			return "", false
		}
		add("Read", pathdisplay.ForWorkspace(cwd, argv[2]))
	case "sed":
		// Only a literal, bounded print is a read preview, not arbitrary
		// sed programs, in-place edits, or input from stdin.
		if len(argv) != 4 || argv[1] != "-n" || argv[3] == "" || strings.HasPrefix(argv[3], "-") {
			return "", false
		}
		spans, ok := sourcePrintSpans(argv[2])
		if !ok {
			return "", false
		}
		add("Read", pathdisplay.ForWorkspace(cwd, argv[3])+" "+strings.Join(spans, " "))
	case "inspect_file":
		options := true
		seen := make(map[string]bool)
		for i := 1; i < len(argv); i++ {
			arg := argv[i]
			if options && arg == "--" {
				options = false
				continue
			}
			if options && strings.HasPrefix(arg, "-") {
				name, value, inline := strings.Cut(arg, "=")
				if seen[name] {
					return "", false
				}
				seen[name] = true
				if name == "--json" && !inline {
					continue
				}
				if name != "--max-tokens" {
					return "", false
				}
				if !inline {
					i++
					if i >= len(argv) {
						return "", false
					}
					value = argv[i]
				}
				if _, valid := sourcePositiveDecimal(value, commandargs.MaxOutputTokens); !valid {
					return "", false
				}
				continue
			}
			if arg == "" || strings.ContainsRune(arg, '\x00') {
				return "", false
			}
			add("Inspect", pathdisplay.ForWorkspace(cwd, arg))
		}
		if len(operations) == 0 {
			return "", false
		}
	case "msymbol":
		arguments, err := commandargs.ParseSymbolQueries(argv[1:])
		if err != nil {
			return "", false
		}
		label := "Read"
		for _, query := range arguments.Queries {
			if query.Mode == "refs" {
				label = "Search"
				break
			}
		}
		add(label, script[int(call.Args[1].Pos().Offset()):int(call.End().Offset())])

	case "ls":
		operands, ok := sourceListOperands(argv)
		if !ok {
			return "", false
		}
		// Keep each operand's source quoting.
		paths := make([]string, len(operands))
		for i, operand := range operands {
			arg := call.Args[operand]
			paths[i] = script[int(arg.Pos().Offset()):int(arg.End().Offset())]
		}
		add("List", cmp.Or(strings.Join(paths, " "), "."))
	case "command":
		if len(argv) != 3 || argv[1] != "-v" || argv[2] == "" || strings.HasPrefix(argv[2], "-") {
			return "", false
		}
		add("Inspect", script[int(call.Pos().Offset()):int(call.End().Offset())])
	case "find":
		for _, arg := range argv[1:] {
			switch arg {
			case "-delete", "-exec", "-execdir", "-ok", "-okdir", "-fprint", "-fprint0", "-fprintf", "-fls":
				return "", false
			}
		}
		if len(argv) < 2 {
			return "", false
		}
		add("Search", script[int(call.Args[1].Pos().Offset()):int(call.End().Offset())])
	case "rg", "grep":
		return sourceSearch(argv, cwd)
	case "skills-mgr":
		if len(argv) >= 3 && argv[1] == "run" {
			add("Skill", script[int(call.Args[1].Pos().Offset()):int(call.End().Offset())])
			break
		}
		if (len(argv) != 3 && len(argv) != 4) || argv[1] != "get" {
			return "", false
		}
		detail := argv[2]
		if len(argv) == 4 {
			detail += " " + argv[3]
		}
		add("Skill", detail)

	default:
		return "", false
	}
	var lines []string
	for _, operation := range operations {
		separator := " "
		if strings.ContainsAny(operation.detail, "\r\n") {
			separator = "\n"
		}
		lines = append(lines, operation.label+separator+SourceCode(operation.detail))
	}
	return strings.Join(lines, "\n\n"), true

}

// Match positive, canonical decimal read options without executing them.
func sourcePositiveDecimal(value string, maximum uint64) (uint64, bool) {
	if value == "" || value[0] == '0' || strings.Trim(value, "0123456789") != "" {
		return 0, false
	}
	number, err := strconv.ParseUint(value, 10, 64)
	return number, err == nil && number <= maximum
}

// sourceListOperands is the argv indexes of ls operands, without its
// options, which describe the listing rather than what is listed. An
// abbreviated option that may take the next word as its value is ambiguous,
// so it stays unclassified.
func sourceListOperands(argv []string) ([]int, bool) {
	valued := []string{"--block-size", "--format", "--hide", "--ignore", "--indicator-style",
		"--quoting-style", "--sort", "--tabsize", "--time", "--time-style", "--width"}
	var operands []int
	for i := 1; i < len(argv); i++ {
		arg := argv[i]
		switch {
		case arg == "--":
			for i++; i < len(argv); i++ {
				operands = append(operands, i)
			}
		case arg == "-" || !strings.HasPrefix(arg, "-"):
			operands = append(operands, i)
		case strings.HasPrefix(arg, "--"):
			if strings.Contains(arg, "=") {
				continue
			}
			if slices.Contains(valued, arg) {
				i++
				continue
			}
			for _, option := range valued {
				if strings.HasPrefix(option, arg) {
					return nil, false
				}
			}
		default:
			// A short option that takes a value ends its cluster; the value is
			// the rest of the word or the next one.
			if at := strings.IndexAny(arg[1:], "ITw"); at >= 0 && at == len(arg)-2 {
				i++
			}
		}
	}
	return operands, true
}
