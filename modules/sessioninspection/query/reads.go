// Source: internal/router/session_corpus_reads.go@[d9f28a74] static read selections.
package query

import (
	"math"
	"path/filepath"
	"strings"

	"github.com/yusing/mekugi/libraries/commandanalysis/shellsyntax"
	"github.com/yusing/mekugi/libraries/commandargs"
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
			arg, literal := shellsyntax.Literal(word)
			if !literal {
				return false
			}
			args = append(args, arg)
		}
		if len(args) < 2 || args[0] != "mcat" {
			return false
		}
		specs, _, err := commandargs.ParseReadBundleSource(args[1:])
		if err != nil {
			return false
		}
		for _, spec := range specs {
			selection := corpusReadSelection{path: filepath.Clean(spec.Path), first: 1, last: math.MaxUint64}
			if spec.Range != "" {
				selection.first, selection.last = uint64(spec.Start), uint64(spec.End)
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
