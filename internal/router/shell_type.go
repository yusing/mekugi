package router

import (
	"context"
	"fmt"
	"os"
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

// executeShellTypeAll lets the interpreter classify functions, aliases and
// keywords in the calling scope, then enumerates executable PATH matches.
func executeShellTypeAll(ctx context.Context, args []string, dispatch string, private func(string) bool) error {
	h := interp.HandlerCtx(ctx)
	mode := ""
	first := 1
	for first < len(args) {
		option := args[first]
		if option == "--" {
			first++
			break
		}
		if !strings.HasPrefix(option, "-") || option == "-" {
			break
		}
		for _, flag := range option[1:] {
			switch flag {
			case 'a':
			case 't', 'p', 'P':
				mode = string(flag)
			default:
				fmt.Fprintf(h.Stderr, "type: invalid option: -%c\n", flag)
				return interp.ExitStatus(2)
			}
		}
		first++
	}
	var result error
	for _, name := range args[first:] {
		if private(name) {
			if mode != "p" && mode != "P" {
				fmt.Fprintln(h.Stdout, "[mekugi-builtin]")
			}
			if err := shellTypePaths(ctx, name, mode, mode != "p" && mode != "P"); err != nil {
				result = err
			}
			continue
		}
		quoted, err := syntax.Quote(name, syntax.LangBash)
		if err != nil {
			return err
		}
		// No authored identifier or shell source is interpolated unquoted.
		// The random dispatch name also avoids invoking shadowed helper builtins.
		script := fmt.Sprintf(`case "$(%s __type_kind %s)" in
file|"") %s __type_paths %s %s false ;;
*) %s __type_first %s %s; %s __type_paths %s %s true ;;
esac`, dispatch, quoted, dispatch, quoted, "'"+mode+"'",
			dispatch, quoted, "'"+mode+"'", dispatch, quoted, "'"+mode+"'")
		if err := h.Builtin(ctx, []string{"eval", script}); err != nil {
			result = err
		}
	}
	return result
}

func shellTypePaths(ctx context.Context, name, mode string, found bool) error {
	h := interp.HandlerCtx(ctx)
	if mode == "p" || mode == "P" {
		found = false
	}
	paths := strings.Split(h.Env.Get("PATH").String(), string(os.PathListSeparator))
	if strings.ContainsRune(name, os.PathSeparator) {
		paths = []string{""}
	}
	for _, directory := range paths {
		path, err := interp.LookPathDir(h.Dir, expand.ListEnviron("PATH="+directory, "PATHEXT="+h.Env.Get("PATHEXT").String()), name)
		if err != nil {
			continue
		}
		found = true
		switch mode {
		case "t":
			fmt.Fprintln(h.Stdout, "file")
		case "p", "P":
			fmt.Fprintln(h.Stdout, path)
		default:
			fmt.Fprintf(h.Stdout, "%s is %s\n", name, path)
		}
	}
	if !found {
		if mode == "" {
			fmt.Fprintf(h.Stderr, "type: %s: not found\n", name)
		}
		return interp.ExitStatus(1)
	}
	return nil
}
