package router

import (
	"io"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"mvdan.cc/sh/v3/syntax"
)

const execProviderBudget = 200 * time.Millisecond
const maxExecProgramBytes = 1 << 20

type execProviderInput struct {
	depth    int
	identity string
	args     []string
	cwd      string
	stdin    string
	deadline time.Time
	changes  execChangeResolver
}

type execProviderResult struct {
	programs  []execProgram
	unhandled bool
	scope     []execScopeEntry
	open      bool
	reason    string
}

type execScopeProvider func(execProviderInput) execProviderResult

// Providers inspect source and metadata only. They never run the user's program.
var execScopeProviders map[string]execScopeProvider

func init() {
	execScopeProviders = map[string]execScopeProvider{
		"python": execPythonScope,
		"node":   execJavaScriptScope,
		"bun":    execJavaScriptScope,
		"deno":   execJavaScriptScope,
		"sh":     execInlineShellScope,
		"bash":   execInlineShellScope,
		"git":    execGitScope,
		"svn":    execSVNScope,
		"hg":     execHGScope,
		"jj":     execJJScope,
	}
	for _, name := range []string{"gofmt", "goimports", "prettier", "eslint", "ruff", "black", "rustfmt", "cargo", "go", "npm", "pnpm", "yarn", "uv", "poetry"} {
		execScopeProviders[name] = execFixerScope
	}
}

func execInlineShellScope(input execProviderInput) execProviderResult {
	for i, arg := range input.args {
		if strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && strings.Contains(arg, "c") && i+1 < len(input.args) {
			plan := classifyExecShellWithin(input.args[i+1], input.cwd, input.identity, input.deadline, input.depth+1, input.changes)
			return execProviderResult{scope: plan.Scope, open: plan.Class == execOpaque, reason: plan.Reason, programs: plan.Programs}
		}
	}
	return execProviderResult{open: true, reason: "shell source is not literal"}
}

func (w *execShellWalker) provider(identity string, args []*syntax.Word) bool {
	key := identity
	if strings.HasPrefix(key, "python") {
		key = "python"
	}
	provider := execScopeProviders[key]
	if provider == nil {
		return false
	}
	if w.depth >= 3 {
		w.opaque("scope provider recursion limit")
		return true
	}
	values, ok := literalArgs(args)
	if !ok {
		w.opaque("provider arguments are dynamic")
		return true
	}
	if len(values) != 0 && (identity == "bun" || identity == "deno") && execPackageSubcommands[values[0]] {
		provider = execFixerScope
	}
	if w.deadline.IsZero() {
		w.deadline = time.Now().Add(execProviderBudget)
	}
	if time.Now().After(w.deadline) {
		w.opaque("scope provider deadline")
		return true
	}
	result := provider(execProviderInput{identity: identity, args: values, cwd: w.cwd, stdin: w.stdin, deadline: w.deadline, depth: w.depth, changes: w.changes})
	if result.unhandled {
		return false
	}
	if time.Now().After(w.deadline) {
		w.opaque("scope provider deadline")
		return true
	}
	if len(result.scope) == 0 {
		w.opaque(result.reason)
		return true
	}
	w.plan.Scope = append(w.plan.Scope, result.scope...)
	w.plan.Programs = append(w.plan.Programs, result.programs...)
	w.plan.label(w.program.Label)
	w.plan.raise(execScoped, result.reason)
	if !slices.Contains(w.plan.Programs, w.program) {
		w.plan.Programs = append(w.plan.Programs, w.program)
	}
	return true
}

// execProgramSource selects literal eval/stdin/script input without evaluating it.
func execProgramSource(input execProviderInput) (source, script, reason string) {
	python := strings.HasPrefix(input.identity, "python")
	for i := 0; i < len(input.args); i++ {
		arg := input.args[i]
		if arg == "-c" && python || !python && (arg == "-e" || arg == "--eval" || arg == "-p" || arg == "--print") {
			if i+1 < len(input.args) {
				return input.args[i+1], "", ""
			}
			return "", "", "missing interpreter source"
		}
		if !python && (strings.HasPrefix(arg, "--eval=") || strings.HasPrefix(arg, "--print=")) {
			_, value, _ := strings.Cut(arg, "=")
			return value, "", ""
		}
		if arg == "-m" && python || arg == "--require" || arg == "-r" || arg == "--import" || arg == "--loader" {
			return "", "", "interpreter loader is not inspected"
		}
		if arg == "-" {
			return input.stdin, "", ""
		}
		if strings.HasPrefix(arg, "-") || (input.identity == "deno" || input.identity == "bun") && arg == "run" {
			continue
		}
		if !filepath.IsAbs(arg) {
			if !filepath.IsAbs(input.cwd) {
				return "", "", "interpreter working directory unavailable"
			}
			arg = filepath.Join(input.cwd, arg)
		}
		file, err := openNativePatchFile(arg)
		if err != nil {
			return "", arg, "interpreter source unavailable"
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			return "", arg, "interpreter source is not regular"
		}
		data, err := io.ReadAll(io.LimitReader(file, maxExecProgramBytes+1))
		if err != nil || len(data) > maxExecProgramBytes {
			return "", arg, "interpreter source exceeds capture bound"
		}
		return string(data), arg, ""
	}
	if input.stdin != "" {
		return input.stdin, "", ""
	}
	return "", "", "interpreter source is not literal"
}

func execProviderPath(cwd, path string) string {
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	if !filepath.IsAbs(cwd) {
		return ""
	}
	return filepath.Join(cwd, path)
}

func execProviderFiles(paths []string, tree bool) execScopeEntry {
	entry := execScopeEntry{Kind: execScopeFile, Through: true}
	if tree {
		entry.Kind, entry.Through = execScopeTree, false
	}
	for _, path := range paths {
		if path != "" {
			entry.Operands = append(entry.Operands, execOperand{Path: path})
		}
	}
	return entry
}
