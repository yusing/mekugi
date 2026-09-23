package router

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"mvdan.cc/sh/v3/syntax"
)

type execQueryOutput struct{ bytes.Buffer }

func (b *execQueryOutput) Write(data []byte) (int, error) {
	if b.Len()+len(data) > maxExecProgramBytes {
		return 0, errors.New("scope query output limit")
	}
	return b.Buffer.Write(data)
}

// Only callers that have validated a read-only query may use this runner. The
// original stock call is never passed to a shell or executed by a provider.
func execScopeQuery(input execProviderInput, name string, args []string, stdin string) (string, error) {
	if !filepath.IsAbs(input.cwd) {
		return "", errors.New("scope query requires an absolute working directory")
	}
	ctx, cancel := context.WithDeadline(context.Background(), input.deadline)
	defer cancel()
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = input.cwd
	command.Stdin = strings.NewReader(stdin)
	command.Stderr = io.Discard
	command.WaitDelay = 10 * time.Millisecond
	var output execQueryOutput
	command.Stdout = &output
	err := command.Run()
	return output.String(), err
}

func (w *execShellWalker) producerPipeline(binary *syntax.BinaryCmd) bool {
	left, lok := binary.X.Cmd.(*syntax.CallExpr)
	right, rok := binary.Y.Cmd.(*syntax.CallExpr)
	if !lok || !rok || len(left.Args) == 0 || len(right.Args) == 0 || len(binary.X.Redirs) != 0 || len(binary.Y.Redirs) != 0 || len(left.Assigns) != 0 || len(right.Assigns) != 0 {
		return false
	}
	producer, pok := execProducerArgs(left.Args)
	consumer, cok := execProducerArgs(right.Args)
	if !pok || !cok || consumer[0] != "xargs" {
		return false
	}
	writer, replacement, ok := execXargsWriter(consumer[1:])
	if !ok {
		w.opaque("xargs scope is not literal")
		return true
	}
	input := execProviderInput{cwd: w.cwd, deadline: w.deadline, depth: w.depth}
	var paths []string
	var err error
	if producer[0] == "find" {
		paths, _, err = execFindPaths(input, producer[1:])
	} else {
		args := slices.Clone(producer[1:])
		name := producer[0]
		if !execReadOnlyProducerArgs(name, args) {
			w.opaque("producer arguments are not verified read-only")
			return true
		}
		switch name {
		case "rg":
			if !slices.Contains(args, "-l") && !slices.Contains(args, "--files-with-matches") {
				return false
			}
			args = append([]string{"--no-config", "--null"}, args...)
		case "grep":
			if !slices.ContainsFunc(args, func(arg string) bool {
				return arg == "--files-with-matches" || strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && strings.Contains(arg, "l")
			}) {
				return false
			}
			args = append([]string{"-Z"}, args...)
		case "fd":
			args = append([]string{"-0"}, args...)
		case "git":
			if len(args) == 0 || args[0] != "ls-files" && args[0] != "grep" {
				return false
			}
			if args[0] == "grep" && !slices.Contains(args, "-l") {
				return false
			}
			args = append([]string{"--no-pager", "--no-optional-locks", "-c", "core.fsmonitor=false", args[0], "-z"}, args[1:]...)
		default:
			return false
		}
		var output string
		output, err = execScopeQuery(input, name, args, "")
		if err == nil {
			for path := range strings.SplitSeq(strings.TrimSuffix(output, "\x00"), "\x00") {
				if path != "" {
					paths = append(paths, execProviderPath(w.cwd, path))
				}
			}
		}
	}
	if err != nil {
		w.opaque("scope producer query failed or exceeded its budget")
		return true
	}
	w.addProducerWriter(input, writer, replacement, paths)
	return true
}

func execXargsWriter(args []string) ([]string, string, bool) {
	replacement := ""
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		flag := args[0]
		args = args[1:]
		switch {
		case flag == "-0" || flag == "--null" || flag == "-r" || flag == "--no-run-if-empty":
		case flag == "-I":
			if len(args) == 0 {
				return nil, "", false
			}
			replacement, args = args[0], args[1:]
		case strings.HasPrefix(flag, "-I"):
			replacement = strings.TrimPrefix(flag, "-I")
		case flag == "-n" || flag == "-P":
			if len(args) == 0 {
				return nil, "", false
			}
			args = args[1:]
		case strings.HasPrefix(flag, "-n") || strings.HasPrefix(flag, "-P"):
		default:
			return nil, "", false
		}
	}
	return args, replacement, len(args) > 0
}

func (w *execShellWalker) addProducerWriter(input execProviderInput, writer []string, replacement string, paths []string) {
	if len(writer) == 0 {
		w.opaque("missing producer writer")
		return
	}
	if !slices.Contains([]string{"sed", "perl", "rm", "touch", "truncate", "gofmt", "goimports", "prettier", "ruff", "black", "rustfmt"}, writer[0]) {
		w.opaque("producer writer is not scoped")
		return
	}
	w.plan.raise(execScoped, "")
	w.plan.label(writer[0])
	for _, path := range paths {
		if time.Now().After(input.deadline) {
			w.opaque("scope provider deadline")
			return
		}
		var words []string
		for _, word := range writer {
			if replacement != "" {
				word = strings.ReplaceAll(word, replacement, path)
			}
			words = append(words, shellQuoteArgument(word))
		}
		if replacement == "" {
			words = append(words, shellQuoteArgument(path))
		}
		plan := classifyExecShellWithin(strings.Join(words, " "), input.cwd, "bash", input.deadline, input.depth+1)
		w.plan.Scope = append(w.plan.Scope, plan.Scope...)
		w.plan.Programs = append(w.plan.Programs, plan.Programs...)
		if plan.Class == execOpaque {
			w.opaque(plan.Reason)
		}
	}
}

func (w *execShellWalker) findProvider(args []*syntax.Word) bool {
	values, ok := execProducerArgs(args)
	if !ok {
		return false
	}
	index := slices.Index(values, "-exec")
	if index < 0 {
		return false
	}
	input := execProviderInput{cwd: w.cwd, deadline: w.deadline, depth: w.depth}
	paths, _, err := execFindPaths(input, values[:index])
	if err != nil {
		w.opaque(err.Error())
		return true
	}
	writer := values[index+1:]
	if len(writer) == 0 {
		w.opaque("missing find writer")
		return true
	}
	writer = writer[:len(writer)-1]
	w.addProducerWriter(input, writer, "{}", paths)
	return true
}

func execProducerArgs(words []*syntax.Word) ([]string, bool) {
	var args []string
	for _, word := range words {
		if word.Lit() == "{}" {
			args = append(args, "{}")
			continue
		}
		values, ok := literalArgs([]*syntax.Word{word})
		if !ok {
			return nil, false
		}
		args = append(args, values[0])
	}
	return args, true
}

func execFindPaths(input execProviderInput, args []string) ([]string, string, error) {
	if len(args) == 0 {
		return nil, "", errors.New("find root is not literal")
	}
	root := execProviderPath(input.cwd, args[0])
	if root == "" || strings.HasPrefix(args[0], "-") {
		return nil, "", errors.New("find root unavailable")
	}
	var name, pattern, kind string
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "-name", "-path", "-type":
			if i+1 == len(args) {
				return nil, root, errors.New("incomplete find test")
			}
			switch args[i] {
			case "-name":
				name = args[i+1]
			case "-path":
				pattern = args[i+1]
			case "-type":
				kind = args[i+1]
			}
			i++
		case "-print", "-print0", "-a", "-and":
		default:
			return nil, root, errors.New("find expression is not statically scoped")
		}
	}
	var paths []string
	entries := 0
	var pathPattern *regexp.Regexp
	if pattern != "" {
		expression, ok := execIgnoreExpression(pattern)
		if !ok {
			return nil, root, errors.New("invalid find path pattern")
		}
		expression = strings.ReplaceAll(expression, "[^/]*", ".*")
		expression = strings.ReplaceAll(expression, "[^/]", ".")
		var err error
		pathPattern, err = regexp.Compile("^" + expression + "$")
		if err != nil {
			return nil, root, err
		}
	}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		entries++
		if entries > maxExecListingEntries || time.Now().After(input.deadline) {
			return errors.New("find scope exceeds budget")
		}
		if kind == "f" && !entry.Type().IsRegular() || kind == "d" && !entry.IsDir() {
			return nil
		}
		if name != "" {
			if matched, _ := filepath.Match(name, entry.Name()); !matched {
				return nil
			}
		}
		if pathPattern != nil {
			relative, _ := filepath.Rel(root, path)
			spelling := args[0]
			if relative != "." {
				spelling = strings.TrimSuffix(spelling, "/") + "/" + filepath.ToSlash(relative)
			}
			if !pathPattern.MatchString(filepath.ToSlash(spelling)) {
				return nil
			}
		}
		paths = append(paths, path)
		return nil
	})
	return paths, root, err
}

// This allowlist parses option boundaries and clusters before a producer runs.
// New tool options default to opaque, not to permission to execute a query.
func execReadOnlyProducerArgs(name string, args []string) bool {
	var flags, values, longFlags, longValues string
	switch name {
	case "rg":
		flags, values = "0lilsSwxvUFPLzau", "efgtT"
		longFlags = "files-with-matches null ignore-case case-sensitive smart-case word-regexp line-regexp invert-match fixed-strings hidden no-ignore no-ignore-vcs no-ignore-parent no-config follow multiline text"
		longValues = "regexp file glob iglob type type-not encoding max-depth"
	case "grep":
		flags, values = "lLrRZiIvwxEFGPsahH", "ef"
		longFlags = "files-with-matches files-without-match recursive dereference-recursive null ignore-case invert-match word-regexp line-regexp fixed-strings extended-regexp perl-regexp no-messages text"
		longValues = "regexp file include exclude exclude-dir binary-files"
	case "fd":
		flags, values = "0HILsifu", "etEd"
		longFlags = "print0 hidden no-ignore no-ignore-vcs no-ignore-parent follow case-sensitive ignore-case full-path absolute-path fixed-strings"
		longValues = "extension type exclude max-depth min-depth search-path base-directory"
	case "git":
		if len(args) == 0 {
			return false
		}
		switch args[0] {
		case "ls-files":
			flags, values = "zcdmosuiktvf", "xX"
			longFlags = "cached deleted modified others ignored stage unmerged killed exclude-standard full-name error-unmatch"
			longValues = "exclude exclude-from exclude-per-directory with-tree"
		case "grep":
			flags, values = "zlLiIvwxEFGPhHa", "ef"
			longFlags = "files-with-matches files-without-match cached no-index untracked exclude-standard no-exclude-standard ignore-case word-regexp invert-match extended-regexp fixed-strings perl-regexp full-name text"
			longValues = "regexp file"
		default:
			return false
		}
		args = args[1:]
	default:
		return false
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return true
		}
		if arg == "-" || !strings.HasPrefix(arg, "-") {
			continue
		}
		if long, ok := strings.CutPrefix(arg, "--"); ok {
			key, _, attached := strings.Cut(long, "=")
			if slices.Contains(strings.Fields(longFlags), key) && !attached {
				continue
			}
			if !slices.Contains(strings.Fields(longValues), key) {
				return false
			}
			if !attached {
				i++
				if i >= len(args) {
					return false
				}
			}
			continue
		}
		for j := 1; j < len(arg); j++ {
			if strings.ContainsRune(values, rune(arg[j])) {
				if j+1 == len(arg) {
					i++
					if i >= len(args) {
						return false
					}
				}
				break
			}
			if !strings.ContainsRune(flags, rune(arg[j])) {
				return false
			}
		}
	}
	return true
}
