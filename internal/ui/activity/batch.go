package activity

import (
	"fmt"
	"slices"
	"strings"
)

// batchTally names how a collapsed batch counts one verb: the past tense it
// shows and what each operation or distinct target counts as.
type batchTally struct{ verb, past, one, many string }

// batchTallies lists the counted verbs in display order.
var batchTallies = []batchTally{
	{"Search", "Searched", "pattern", "patterns"},
	{"Read", "Read", "file", "files"},
	{"List", "Listed", "path", "paths"},
	{"Inspect", "Inspected", "target", "targets"},
	{"Skill", "Loaded", "skill", "skills"},
	{"Run", "Ran", "command", "commands"},
}

// CollapseBatch folds a settled run of operations into one
// "batch" row whose Members are the operations the output dialog pages
// through. Running or skipped work and questions keep their
// rows, as does a lone operation.
func CollapseBatch(blocks []Block) (Block, bool) {
	var members []Block
	operations := 0
	for _, block := range blocks {
		switch {
		case block.Kind == "filter":
			continue
		case block.Kind != "op" && block.Kind != "reads", block.Verb == "", block.Verb == "Attach failed", block.Running, block.Skipped, block.Approval != "", block.JournalTransport,
			block.EditSource != "" && editHeading(block) != "Edited", len(block.Questions) > 0:
			return Block{}, false
		}
		if !block.BatchExit {
			operations++
		}
		if len(block.Members) > 0 {
			members = append(members, block.Members...)
		} else {
			members = append(members, block)
		}
	}
	if operations < 2 {
		return Block{}, false
	}
	return Block{Kind: "batch", Members: members, Collapsed: true}, true
}

// batchRow is a collapsed batch's one row: each verb's count, colored like
// that verb's own rows.
func (p *Painter) batchRow(block Block, width int) []string {
	type tally struct {
		order       int
		verb, label string
		targets     []string
		count       int
	}
	var tallies []*tally
	for _, member := range block.Members {
		if member.BatchExit {
			continue
		}
		verb := member.Verb
		if verb == "Skill" && member.Kind != "reads" {
			verb = "" // A skill program runs rather than loads.
		}
		order := slices.IndexFunc(batchTallies, func(t batchTally) bool { return t.verb == verb })
		if order < 0 {
			order = len(batchTallies)
		}
		at := slices.IndexFunc(tallies, func(t *tally) bool { return t.verb == member.Verb && t.order == order })
		if at < 0 {
			tallies = append(tallies, &tally{order: order, verb: member.Verb, label: RowVerb(member)})
			at = len(tallies) - 1
		}
		t := tallies[at]
		if member.Kind != "reads" {
			t.count++
			continue
		}
		for _, read := range member.Reads {
			if !slices.Contains(t.targets, read.Path) {
				t.targets = append(t.targets, read.Path)
			}
		}
	}
	slices.SortStableFunc(tallies, func(a, b *tally) int { return a.order - b.order })
	parts := make([]string, 0, len(tallies))
	for _, t := range tallies {
		count := t.count + len(t.targets)
		verb, rest := t.label, ""
		if count > 1 {
			rest = fmt.Sprintf(" ×%d", count)
		}
		if t.order < len(batchTallies) {
			name := batchTallies[t.order]
			noun := name.many
			if count == 1 {
				noun = name.one
			}
			verb, rest = name.past, fmt.Sprintf(" %d %s", count, noun)
		}
		parts = append(parts, VerbColor(t.verb)+"\x1b[1m"+verb+Reset+rest)
	}
	failed := 0
	seen := make(map[uint64]bool)
	for _, member := range block.Members {
		if member.ExitCode != 0 && (member.Source == 0 || !seen[member.Source]) {
			failed++
			seen[member.Source] = true
		}
	}
	if failed > 0 {
		noun := "commands"
		if failed == 1 {
			noun = "command"
		}
		parts = append(parts, Red+fmt.Sprintf("%d failed %s", failed, noun)+Reset)
	}
	row := strings.Join(parts, Dim+" • "+Undim)
	if block.Hovered {
		row = Underline(row)
	}
	return Wrap(row, width, false)
}
