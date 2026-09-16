package router

import (
	"math"
	"path/filepath"
	"strconv"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

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
		if len(args) < 2 || args[0] != "hcat" {
			return false
		}
		args = args[1:]
		for len(args) > 0 && strings.HasPrefix(args[0], "-") {
			if args[0] == "--" {
				args = args[1:]
				break
			}
			if args[0] != "--max-tokens" || len(args) < 2 {
				return false
			}
			args = args[2:]
		}
		if len(args) < 1 || len(args) > 2 {
			return false
		}
		selection := corpusReadSelection{path: filepath.Clean(args[0]), first: 1, last: math.MaxUint64}
		if len(args) == 2 {
			first, last, ok := strings.Cut(args[1], ":")
			if !ok {
				return false
			}
			var e1, e2 error
			selection.first, e1 = strconv.ParseUint(first, 10, 64)
			selection.last, e2 = strconv.ParseUint(last, 10, 64)
			if e1 != nil || e2 != nil || selection.first > selection.last {
				return false
			}
			selection.first = max(1, selection.first)
		}
		selections = append(selections, selection)
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
