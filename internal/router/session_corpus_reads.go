package router

import (
	"encoding/json"
	"math"
	"path/filepath"
	"strconv"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

func corpusReadSelectionsForCall(call sessionInspectionCall) []corpusReadSelection {
	readCommand := func(input string) []corpusReadSelection {
		var arguments struct {
			Command string `json:"cmd"`
		}
		if json.Unmarshal([]byte(input), &arguments) != nil {
			return nil
		}
		return corpusReadSelections(arguments.Command)
	}
	if call.item.Name == "exec_command" {
		return readCommand(call.item.Arguments)
	}
	if call.item.Name != "exec" {
		return nil
	}
	calls, ok := toolActivityUnwrapExecCalls(call.item.Input, false)
	if !ok {
		return nil
	}
	var selections []corpusReadSelection
	for _, nested := range calls {
		if jsonString(nested, "name") == "exec_command" {
			selections = append(selections, readCommand(jsonString(nested, "arguments"))...)
		}
	}
	return selections
}

type corpusReadSelection struct {
	path        string
	first, last uint64
}

// These are static candidates, not executed-read counts or proof of unchanged
// source. Never decode carriers, expand variables, or execute shell programs.
func corpusReadSelections(script string) []corpusReadSelection {
	program, err := syntax.NewParser().Parse(strings.NewReader(script), "")
	if err != nil {
		return nil
	}
	var selections []corpusReadSelection
	syntax.Walk(program, func(node syntax.Node) bool {
		call, ok := node.(*syntax.CallExpr)
		if !ok {
			return true
		}
		var args []string
		for _, word := range call.Args {
			arg, literal := shellCatLiteral(word)
			if !literal {
				return false
			}
			args = append(args, arg)
		}
		if len(args) < 2 || args[0] != "mcat" {
			return false
		}
		specs, _, err := parseReadBundle(args[1:])
		if err != nil {
			return false
		}
		for _, spec := range specs {
			selection := corpusReadSelection{path: filepath.Clean(spec.path), first: 1, last: math.MaxUint64}
			if spec.span != "" {
				first, last, _ := strings.Cut(spec.span, ":")
				var e1, e2 error
				selection.first, e1 = strconv.ParseUint(first, 10, 64)
				selection.last, e2 = strconv.ParseUint(last, 10, 64)
				if e1 != nil || e2 != nil || selection.first > selection.last {
					return false
				}
				selection.first = max(1, selection.first)
			}
			selections = append(selections, selection)
		}
		return false
	})
	return selections
}

func corpusReadOverlap(a, b []corpusReadSelection) bool {
	for _, left := range a {
		for _, right := range b {
			if left.path == right.path && left.first <= right.last && right.first <= left.last {
				return true
			}
		}
	}
	return false
}
