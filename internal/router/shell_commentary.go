package router

import (
	"context"
	"errors"
	"strings"

	"mvdan.cc/sh/v3/interp"
)

type shellJournalCommand struct {
	Op       string
	Mutation *journalMutation
	Batch    []journalMutation
	Agent    string
}

type shellJournalResult struct {
	IDs             []string
	Items           []journalListItem
	FinishRequested bool
}

func shellCommentaryCallHandler(sink shellCommentarySink) interp.CallHandlerFunc {
	return func(ctx context.Context, arguments []string) ([]string, error) {
		if len(arguments) == 0 || arguments[0] != commentaryArgumentName {
			return arguments, nil
		}
		command, err := parseShellJournalCommand(arguments)
		if err != nil {
			return nil, err
		}
		if sink == nil {
			return nil, errors.New("journal publisher unavailable")
		}
		result, err := sink.RequestJournal(ctx, command)
		if err != nil {
			return nil, err
		}
		switch command.Op {
		case "add":
			if len(result.IDs) != 1 {
				return nil, errors.New("journal publisher did not return an add ID")
			}
			return []string{"command", "printf", "%s\\n", result.IDs[0]}, nil
		case "list":
			return shellJournalListCommand(result), nil
		default:
			// Other successful mutations intentionally produce no output.
			return []string{"command", "true"}, nil
		}
	}
}

func shellJournalListCommand(result shellJournalResult) []string {
	payload := map[string]any{"ok": true, "items": result.Items}
	return []string{"command", "printf", "%s\\n", string(mustMarshalJSON(payload))}
}

// Shell expansion supplies each operand as one argv value. Flags are accepted
// only after the operation operands, so text remains one exact argv value.
func parseShellJournalCommand(arguments []string) (shellJournalCommand, error) {
	var command shellJournalCommand
	if len(arguments) < 2 {
		return command, errors.New("journal requires list, add, edit, delete, batch, or finish")
	}
	command.Op = arguments[1]
	operands := 0
	switch command.Op {
	case "add", "delete", "batch":
		operands = 1
	case "edit":
		operands = 2
	case "list", "finish":
		if len(arguments) > 2 && !strings.HasPrefix(arguments[2], "--") {
			operands = 1
		}
	default:
		return command, errors.New("unknown journal operation " + command.Op)
	}
	if len(arguments) < 2+operands {
		return command, errors.New("journal operation is missing required operands")
	}
	args := arguments[1 : 2+operands]
	var answer *bool
	var reportNow bool
	for _, flag := range arguments[2+operands:] {
		switch flag {
		case "--report-now":
			reportNow = true
		case "--answer", "--clear-answer":
			if answer != nil {
				return command, errors.New("journal accepts only one answer option")
			}
			answer = new(flag == "--answer")
		default:
			return command, errors.New("unknown journal option " + flag)
		}
	}
	switch command.Op {
	case "list":
		if len(args) == 2 && args[1] == "" {
			return command, errors.New("use journal list [AGENT]")
		}
		if len(args) == 2 {
			command.Agent = args[1]
		}
		if answer != nil || reportNow {
			return command, errors.New("journal list does not accept mutation options")
		}
	case "add":
		if args[1] == "" {
			return command, errors.New("use journal add TEXT [--answer|--clear-answer] [--report-now]")
		}
		command.Mutation = &journalMutation{Op: "add", Text: new(args[1]), Answer: answer, ReportNow: reportNow}
	case "edit":
		if args[1] == "" || args[2] == "" {
			return command, errors.New("use journal edit ID TEXT [--answer|--clear-answer] [--report-now]")
		}
		command.Mutation = &journalMutation{Op: "edit", ID: args[1], Text: new(args[2]), Answer: answer, ReportNow: reportNow}
	case "delete":
		if args[1] == "" || answer != nil {
			return command, errors.New("use journal delete ID [--report-now]")
		}
		command.Mutation = &journalMutation{Op: "delete", ID: args[1], ReportNow: reportNow}
	case "batch":
		if answer != nil || reportNow {
			return command, errors.New("use journal batch JSON_ARRAY")
		}
		var err error
		command.Batch, err = decodeJournalMutations([]byte(args[1]))
		if err != nil {
			return command, errors.New("journal batch requires a JSON mutation array")
		}
	case "finish":
		if answer != nil || reportNow {
			return command, errors.New("use journal finish [JSON_ARRAY]")
		}
		if len(args) == 2 {
			var err error
			command.Batch, err = decodeJournalMutations([]byte(args[1]))
			if err != nil {
				return command, errors.New("journal finish requires a JSON mutation array")
			}
		}
	}
	return command, nil
}
