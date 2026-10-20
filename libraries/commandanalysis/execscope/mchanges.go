package execscope

import (
	"time"

	"github.com/yusing/mekugi/libraries/commandanalysis/shellsyntax"

	"mvdan.cc/sh/v3/syntax"
)

// ChangeResolver resolves literal mutation arguments in the selected directory.
// InvalidArguments distinguishes parser rejection from unavailable retained paths.
type ChangeResolver func(arguments []string, directory string, deadline time.Time) ChangeResolution

type ChangeResolution struct {
	Paths            []string
	InvalidArguments bool
}

// mchanges revert and apply write only the paths their selected records name,
// so they are declared writers. Reads remain neutral.
func (w *execShellWalker) mchanges(args []*syntax.Word) {
	if len(args) == 0 {
		return
	}
	// Only the first word selects a mutation, so a read may still expand its
	// operands.
	first, glob, ok := shellsyntax.Operand(args[0])
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
	values, ok := shellsyntax.Args(args)
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
	if w.deadline.IsZero() {
		w.deadline = time.Now().Add(Budget)
	}
	resolved := w.changes(values, w.cwd, w.deadline)
	if resolved.InvalidArguments {
		w.opaque(subcommand + " arguments are not valid")
		return
	}
	if len(resolved.Paths) == 0 {
		w.opaque(subcommand + " history is unavailable")
		return
	}
	paths := resolved.Paths
	w.plan.raise(Declared, "")
	w.plan.label(subcommand)
	w.plan.add(execProviderFiles(paths, false))
}
