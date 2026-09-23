package router

import (
	"errors"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

func execGitQuery(input execProviderInput, args []string, stdin string) (string, error) {
	return execScopeQuery(input, "git", append([]string{"--no-optional-locks", "-c", "core.fsmonitor=false"}, args...), stdin)
}

func execGitScope(input execProviderInput) execProviderResult {
	args := slices.Clone(input.args)
	for len(args) >= 2 && args[0] == "-C" {
		input.cwd = execProviderPath(input.cwd, args[1])
		args = args[2:]
	}
	if len(args) == 0 {
		return execProviderResult{unhandled: true}
	}
	command := args[0]
	args = args[1:]
	if !slices.Contains([]string{"restore", "checkout", "switch", "reset", "stash", "clean", "apply", "am", "cherry-pick", "revert", "merge"}, command) {
		return execProviderResult{unhandled: true}
	}
	if command == "restore" && slices.Contains(args, "--staged") && !slices.Contains(args, "--worktree") || command == "reset" && !slices.Contains(args, "--hard") || command == "stash" && len(args) > 0 && slices.Contains([]string{"list", "show", "drop", "clear"}, args[0]) {
		return execProviderResult{unhandled: true}
	}
	if !filepath.IsAbs(input.cwd) {
		return execProviderResult{open: true, reason: "VCS working directory unavailable"}
	}
	var operands []string
	if at := slices.Index(args, "--"); at >= 0 {
		operands = args[at+1:]
	}
	fallback := func(reason string) execProviderResult {
		var paths []string
		for _, path := range operands {
			if !strings.HasPrefix(path, ":(") {
				paths = append(paths, execProviderPath(input.cwd, path))
			}
		}
		result := execProviderResult{open: true, reason: reason}
		if len(paths) != 0 {
			result.scope = []execScopeEntry{execProviderFiles(paths, true)}
		}
		return result
	}
	filterConfigured := func() bool {
		output, err := execGitQuery(input, []string{"config", "--get-regexp", `^filter\.`}, "")
		if err != nil {
			if exit, ok := errors.AsType[*exec.ExitError](err); ok && exit.ExitCode() == 1 {
				return false
			}
			return true
		}
		return output != ""
	}
	outputRoot := input.cwd
	if command != "clean" && command != "apply" && command != "am" {
		root, err := execGitQuery(input, []string{"rev-parse", "--show-toplevel"}, "")
		if err != nil {
			return fallback("Git worktree root unavailable")
		}
		outputRoot = strings.TrimSuffix(root, "\n")
		if !filepath.IsAbs(outputRoot) {
			return fallback("Git worktree root unavailable")
		}
	}
	diff := []string{"diff", "--no-relative", "--no-ext-diff", "--no-textconv", "--name-only", "-z"}
	var query, extra []string
	open := false
	source := ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if (arg == "--source" || arg == "-s") && i+1 < len(args) {
			source = args[i+1]
			i++
			continue
		}
		if after, ok := strings.CutPrefix(arg, "--source="); ok {
			source = after
		}
	}
	if strings.HasPrefix(source, "-") {
		return fallback("revision cannot be used as a query option")
	}
	switch command {
	case "restore":
		if len(operands) == 0 {
			for _, arg := range args {
				if !strings.HasPrefix(arg, "-") && arg != source {
					operands = append(operands, arg)
				}
			}
		}
		if source == "" && slices.Contains(args, "--staged") {
			source = "HEAD"
		}
		if filterConfigured() {
			return fallback("configured clean filters prevent worktree queries")
		}
		query = slices.Clone(diff)
		if source != "" {
			query = append(query, source)
		}
		query = append(query, "--")
		query = append(query, operands...)
	case "checkout":
		if len(operands) > 0 {
			for _, arg := range args {
				if arg == "--" {
					break
				}
				if !strings.HasPrefix(arg, "-") {
					source = arg
					break
				}
			}
			if filterConfigured() {
				return fallback("configured clean filters prevent worktree queries")
			}
			query = slices.Clone(diff)
			if source != "" {
				query = append(query, source)
			}
			query = append(query, "--")
			query = append(query, operands...)
			break
		}
		fallthrough
	case "switch":
		if len(args) == 0 || strings.HasPrefix(args[0], "-") {
			return fallback("branch target is not literal")
		}
		branch := args[0]
		if _, err := execGitQuery(input, []string{"rev-parse", "--verify", "--quiet", branch + "^{commit}"}, ""); err != nil {
			return fallback("branch target is not a commit")
		}
		query = append(slices.Clone(diff), "HEAD", branch, "--")
	case "reset":
		if filterConfigured() {
			return fallback("configured clean filters prevent worktree queries")
		}
		for _, arg := range args {
			if !strings.HasPrefix(arg, "-") {
				source = arg
				break
			}
		}
		if source == "" {
			source = "HEAD"
		}
		query = slices.Clone(diff)
		if source != "" {
			query = append(query, source)
		}
		query = append(query, "--")
	case "stash":
		sub := "push"
		if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
			sub = args[0]
			args = args[1:]
		}
		if sub == "pop" || sub == "apply" {
			ref := "stash@{0}"
			for _, arg := range args {
				if !strings.HasPrefix(arg, "-") {
					ref = arg
					break
				}
			}
			query = []string{"stash", "show", "--no-relative", "--no-ext-diff", "--no-textconv", "--name-only", "-z", "--include-untracked", ref}
		} else if sub == "push" || sub == "save" {
			if filterConfigured() {
				return fallback("configured clean filters prevent worktree queries")
			}
			query = append(slices.Clone(diff), "HEAD", "--")
			query = append(query, operands...)
			if slices.Contains(args, "-u") || slices.Contains(args, "--include-untracked") {
				extra = []string{"ls-files", "-o", "--exclude-standard", "--full-name", "-z"}
			}
		} else {
			return execProviderResult{unhandled: true}
		}
	case "clean":
		query = []string{"clean", "-n"}
		for i := 0; i < len(args); i++ {
			arg := args[i]
			if arg == "--" {
				query = append(query, args[i:]...)
				break
			}
			if arg == "-e" || arg == "--exclude" {
				if i+1 == len(args) {
					return fallback("clean exclusion is missing")
				}
				query = append(query, "-e", args[i+1])
				i++
				continue
			}
			if strings.HasPrefix(arg, "--exclude=") || strings.HasPrefix(arg, "-e") {
				query = append(query, arg)
				continue
			}
			if strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") {
				for _, flag := range strings.TrimPrefix(arg, "-") {
					if strings.ContainsRune("dxX", flag) {
						query = append(query, "-"+string(flag))
					}
				}
			} else if arg != "--force" && arg != "--dry-run" {
				query = append(query, arg)
			}
		}
	case "apply", "am":
		query = []string{"apply", "--numstat", "--summary", "-z"}
		for i := 0; i < len(args); i++ {
			arg := args[i]
			if strings.HasPrefix(arg, "--directory=") || strings.HasPrefix(arg, "-p") {
				query = append(query, arg)
			} else if !strings.HasPrefix(arg, "-") {
				query = append(query, arg)
			}
		}
		if len(query) == 4 && input.stdin == "" {
			return fallback("patch input is unavailable")
		}
	case "cherry-pick", "revert":
		if len(args) == 0 || strings.HasPrefix(args[0], "-") {
			return fallback("revision is not literal")
		}
		query = append(slices.Clone(diff), args[0]+"^", args[0], "--")
		open = true
	case "merge":
		if len(args) == 0 || strings.HasPrefix(args[0], "-") {
			return fallback("merge target is not literal")
		}
		query = append(slices.Clone(diff), "HEAD..."+args[0], "--")
		open = true
	}
	output, err := execGitQuery(input, query, input.stdin)
	if err != nil {
		return fallback("VCS scope query failed or exceeded its budget")
	}
	if len(extra) > 0 {
		more, err := execGitQuery(input, extra, "")
		if err != nil {
			return fallback("untracked scope query failed")
		}
		output += more
	}
	var paths []string
	if command == "clean" {
		for line := range strings.SplitSeq(output, "\n") {
			if path, ok := strings.CutPrefix(line, "Would remove "); ok {
				if decoded, err := strconv.Unquote(path); err == nil {
					path = decoded
				}
				paths = append(paths, execProviderPath(input.cwd, strings.TrimSuffix(path, "/")))
			}
		}
	} else if command == "apply" || command == "am" {
		paths = execGitApplyPaths(output, input.cwd)
	} else {
		for path := range strings.SplitSeq(output, "\x00") {
			if path != "" {
				paths = append(paths, execProviderPath(outputRoot, path))
			}
		}
	}
	entry := execProviderFiles(paths, true)
	if slices.Contains([]string{"switch", "merge", "cherry-pick", "revert"}, command) || command == "checkout" && len(operands) == 0 || command == "stash" && len(input.args) > 1 && (input.args[1] == "pop" || input.args[1] == "apply") {
		entry.Origin = "git " + command
	}
	result := execProviderResult{scope: []execScopeEntry{entry}, open: open}
	if open {
		result.reason = "conflict resolution may write additional paths"
	}
	return result
}

func execGitApplyPaths(output, cwd string) []string {
	var paths []string
	parts := strings.Split(output, "\x00")
	for i := 0; i < len(parts); i++ {
		fields := strings.SplitN(parts[i], "\t", 3)
		if len(fields) != 3 {
			continue
		}
		if _, err := strconv.Atoi(fields[0]); err != nil && fields[0] != "-" {
			continue
		}
		if fields[2] != "" {
			paths = append(paths, execProviderPath(cwd, fields[2]))
			continue
		}
		// -z emits empty pathname followed by both rename operands.
		if i+2 < len(parts) {
			paths = append(paths, execProviderPath(cwd, parts[i+1]), execProviderPath(cwd, parts[i+2]))
			i += 2
		}
	}
	// git apply's numstat names only the destination of a rename, unlike
	// git diff's NUL-triplet form. --summary retains the missing source.
	for line := range strings.SplitSeq(strings.ReplaceAll(output, "\x00", "\n"), "\n") {
		body, ok := strings.CutPrefix(line, " rename ")
		if !ok {
			continue
		}
		body, _, ok = strings.CutLast(body, " (")
		if !ok {
			continue
		}
		before, after, ok := strings.Cut(body, " => ")
		if !ok {
			continue
		}
		if start := strings.LastIndexByte(before, '{'); start >= 0 {
			if end := strings.IndexByte(after, '}'); end >= 0 {
				prefix, suffix := before[:start], after[end+1:]
				before, after = prefix+before[start+1:]+suffix, prefix+after[:end]+suffix
			}
		}
		for _, path := range []string{before, after} {
			if decoded, err := strconv.Unquote(path); err == nil {
				path = decoded
			}
			paths = append(paths, execProviderPath(cwd, path))
		}
	}
	return paths
}

func execSVNScope(input execProviderInput) execProviderResult {
	if len(input.args) == 0 || input.args[0] != "revert" {
		return execProviderResult{unhandled: true}
	}
	if !filepath.IsAbs(input.cwd) {
		return execProviderResult{open: true, reason: "VCS working directory unavailable"}
	}
	depth := "empty"
	var operands []string
	for _, arg := range input.args[1:] {
		if arg == "-R" || arg == "--recursive" {
			depth = "infinity"
		} else if !strings.HasPrefix(arg, "-") {
			operands = append(operands, arg)
		}
	}
	query := append([]string{"status", "-q", "--depth", depth, "--"}, operands...)
	output, err := execScopeQuery(input, "svn", query, "")
	var paths []string
	if err == nil {
		for line := range strings.SplitSeq(output, "\n") {
			if len(line) > 8 {
				paths = append(paths, execProviderPath(input.cwd, line[8:]))
			}
		}
	} else {
		for _, path := range operands {
			paths = append(paths, execProviderPath(input.cwd, path))
		}
	}
	result := execProviderResult{scope: []execScopeEntry{execProviderFiles(paths, depth == "infinity")}, open: err != nil}
	if err != nil {
		result.reason = "Subversion status query unavailable"
	}
	return result
}

func execHGScope(input execProviderInput) execProviderResult {
	if len(input.args) == 0 || input.args[0] != "revert" {
		return execProviderResult{unhandled: true}
	}
	if !filepath.IsAbs(input.cwd) {
		return execProviderResult{open: true, reason: "VCS working directory unavailable"}
	}
	if slices.Contains(input.args, "--all") {
		return execProviderResult{open: true, reason: "Mercurial --all has no read-only worktree scope query"}
	}
	var paths []string
	for _, arg := range input.args[1:] {
		if strings.HasPrefix(arg, "-") {
			continue
		}
		path := execProviderPath(input.cwd, arg)
		matches := []string{path}
		if strings.ContainsAny(path, "*?[") {
			matches, _ = filepath.Glob(path)
		}
		for _, path := range matches {
			paths = append(paths, path)
			if !slices.Contains(input.args, "-C") && !slices.Contains(input.args, "--no-backup") {
				paths = append(paths, path+".orig")
			}
		}
	}
	return execProviderResult{scope: []execScopeEntry{execProviderFiles(paths, true)}, open: true, reason: "Mercurial backup locations are configurable; no read-only worktree query"}
}

func execJJScope(input execProviderInput) execProviderResult {
	if len(input.args) == 0 || input.args[0] != "restore" {
		return execProviderResult{unhandled: true}
	}
	var paths []string
	for i := 1; i < len(input.args); i++ {
		arg := input.args[i]
		if slices.Contains([]string{"--from", "--to", "-r", "--revision"}, arg) {
			i++
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		if path := execProviderPath(input.cwd, arg); path != "" {
			paths = append(paths, path)
		}
	}
	entry := execProviderFiles(paths, true)
	entry.Origin = "jj restore"
	return execProviderResult{scope: []execScopeEntry{entry}, open: true, reason: "Jujutsu snapshots worktree state; no read-only query"}
}
