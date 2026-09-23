package router

import "strings"

// Source: codex-rs/shell-command/src/parse_command.rs:1787:1842,2349:2389
// and codex-rs/tui/src/exec_cell/render.rs:384:392@6f6af0fce4381233fa2e83dc24641167e68dfcb5.
// Display query and target instead of execution flags. Preserve authored paths
// and all explicit patterns; this projection never changes the shell command.
func toolActivitySearch(argv []string) (string, bool) {
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
				kind := toolActivitySearchFlag(argv[0], flag)
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
		target := "."
		if len(paths) != 0 {
			target = strings.Join(paths, " ")
		}
		return "List " + toolActivityCode(target), true
	}
	if !explicit && len(paths) != 0 {
		queries, paths = paths[:1], paths[1:]
	}
	if len(queries) == 0 {
		return "", false
	}
	var display strings.Builder
	display.WriteString("Search " + toolActivityCode(strings.Join(queries, " | ")))
	if len(paths) != 0 {
		for i, path := range paths {
			if i == 0 {
				display.WriteString(" in ")
			} else {
				display.WriteString(" ")
			}
			display.WriteString(toolActivityCode(path))
		}
	}
	return display.String(), true
}

// 0: switch, 1: option value, 2: pattern value, 3: file listing.
// Unknown arity keeps the original Run preview rather than guessing a query.
func toolActivitySearchFlag(command, flag string) int {
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
