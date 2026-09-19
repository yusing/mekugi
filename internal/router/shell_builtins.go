package router

import (
	"context"
	"fmt"
	"strings"

	"mvdan.cc/sh/v3/interp"
)

func executeShellBuiltin(ctx context.Context, args []string, privateTools map[string]toolContribution, terminal bool, dispatch string) error {
	h := interp.HandlerCtx(ctx)
	switch args[0] {
	case "ulimit":
		return executeShellUlimit(ctx, args)
	case "printf":
		return executeShellPrintf(ctx, args)
	case "read":
		return executeShellRead(ctx, args)
	case "__type_kind":
		return h.Builtin(ctx, []string{"type", "-t", "--", args[1]})
	case "__type_first":
		if args[2] == "p" || args[2] == "P" {
			return nil
		}
		options := []string{"type"}
		if args[2] == "t" {
			options = append(options, "-t")
		}
		return h.Builtin(ctx, append(options, "--", args[1]))
	case "__type_paths":
		return shellTypePaths(ctx, args[1], args[2], args[3] == "true")
	}
	if args[0] == "kill" || args[0] == "time" {
		// Calling the external owner directly bypasses unsupported or incomplete
		// builtins without display routing or an extra interpreter.
		return runExternalShellCommand(ctx, args, terminal, h)
	}
	isPrivate := func(name string) bool {
		switch name {
		case "journal", "hpatch", "hread", "hchanges", "hrun", "type", "kill", "printf", "read", "ulimit":
			return true
		}
		_, ok := privateTools[name]
		return ok
	}
	for _, option := range args[1:] {
		if option == "--" || !strings.HasPrefix(option, "-") {
			break
		}
		if strings.Contains(option[1:], "a") {
			return executeShellTypeAll(ctx, args, dispatch, isPrivate)
		}
	}
	first := 1
	for first < len(args) && (strings.HasPrefix(args[first], "-") || strings.HasPrefix(args[first], "+")) {
		first++
		if args[first-1] == "--" {
			break
		}
	}
	// Let the interpreter validate options once, before emitting any result.
	if err := h.Builtin(ctx, args[:first]); err != nil {
		return err
	}
	options := append([]string{}, args[:first]...)
	if options[len(options)-1] != "--" {
		options = append(options, "--")
	}
	var result error
	for _, name := range args[first:] {
		if isPrivate(name) {
			if _, err := fmt.Fprintln(h.Stdout, "[mekugi-builtin]"); err != nil {
				return err
			}
		} else if err := h.Builtin(ctx, append(options[:len(options):len(options)], name)); err != nil {
			result = err
		}
	}
	return result
}
