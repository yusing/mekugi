package router

import (
	"context"
	"errors"

	"mvdan.cc/sh/v3/interp"
)

func shellCommentaryCallHandler(sink shellCommentarySink) interp.CallHandlerFunc {
	return func(ctx context.Context, arguments []string) ([]string, error) {
		if arguments[0] != commentaryArgumentName {
			return arguments, nil
		}
		mutation, err := parseShellJournal(arguments)
		if err != nil {
			return nil, err
		}
		if sink == nil {
			return nil, errors.New("journal publisher unavailable")
		}
		if err := sink.Publish(ctx, string(mustMarshalJSON(mutation))); err != nil {
			return nil, err
		}
		// `command true` bypasses any user-defined function named true while
		// preserving the shell's normal redirection behavior for this command.
		return []string{"command", "true"}, nil
	}
}

// Shell expansion supplies each operand as one argv value. The optional flag
// follows all operands, so text needs no secondary escaping or parsing.
func parseShellJournal(arguments []string) (journalMutation, error) {
	var mutation journalMutation
	if len(arguments) < 2 {
		return mutation, errors.New("journal requires add, edit, or delete")
	}
	args := arguments[1:]
	if len(args) > 1 && args[len(args)-1] == "--report-now" {
		mutation.ReportNow = true
		args = args[:len(args)-1]
	}
	mutation.Op = args[0]
	switch {
	case mutation.Op == "add" && len(args) == 2:
		mutation.Text = new(args[1])
	case mutation.Op == "edit" && len(args) == 3:
		mutation.ID, mutation.Text = args[1], new(args[2])
	case mutation.Op == "delete" && len(args) == 2:
		mutation.ID = args[1]
	default:
		return journalMutation{}, errors.New("use journal add TEXT, journal edit ID TEXT, or journal delete ID; optionally append --report-now")
	}
	return mutation, nil
}
