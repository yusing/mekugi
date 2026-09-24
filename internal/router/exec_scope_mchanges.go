package router

import (
	"context"
	"time"

	"mvdan.cc/sh/v3/syntax"
)

// execChangeResolver lists the absolute paths recorded by selected changes,
// reading only the change store before the deadline.
type execChangeResolver func(options changeReadOptions, deadline time.Time) ([]string, error)

// storeChangeResolver reads the calling thread's change namespace.
func storeChangeResolver(ctx context.Context, store *mekugiReplayStore) execChangeResolver {
	return func(options changeReadOptions, deadline time.Time) ([]string, error) {
		ctx, cancel := context.WithDeadline(ctx, deadline)
		defer cancel()
		return store.changeMutationPaths(ctx, options)
	}
}

// mchanges revert and apply write only the paths their selected records name,
// so they are declared writers. Reads remain neutral.
func (w *execShellWalker) mchanges(args []*syntax.Word) {
	if len(args) == 0 {
		return
	}
	// Only the first word selects a mutation, so a read may still expand its
	// operands.
	first, glob, ok := execWordOperand(args[0])
	if !ok || glob {
		w.opaque("dynamic mchanges subcommand")
		return
	}
	if first != "revert" && first != "apply" {
		for _, arg := range args {
			if !execWordStatic(arg) {
				w.opaque("command substitution")
				return
			}
		}
		return
	}
	values, ok := literalArgs(args)
	if !ok {
		w.opaque("dynamic mchanges operand")
		return
	}
	subcommand := "mchanges " + values[0]
	if w.cwd == "" {
		w.opaque(subcommand + " in an unknown directory")
		return
	}
	if w.changes == nil {
		w.opaque(subcommand + " history is unavailable")
		return
	}
	options, err := parseChangeRead(values, w.cwd)
	if err != nil {
		w.opaque(subcommand + " arguments are not valid")
		return
	}
	if w.deadline.IsZero() {
		w.deadline = time.Now().Add(execProviderBudget)
	}
	paths, err := w.changes(options, w.deadline)
	if err != nil || len(paths) == 0 {
		w.opaque(subcommand + " history is unavailable")
		return
	}
	w.plan.raise(execDeclared, "")
	w.plan.label(subcommand)
	w.plan.add(execProviderFiles(paths, false))
}
