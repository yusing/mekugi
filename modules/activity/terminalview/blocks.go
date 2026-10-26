package terminalview

import (
	markdownsyntax "github.com/yusing/mekugi/libraries/sourceanalysis/markdownsyntax"

	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	"github.com/yusing/mekugi/modules/activity/presentation"
)

// Block adds terminal disclosure and geometry to a renderer-neutral value.
// OutputSnapshot is populated only for a scoped paint or detail request.
// Cached feed values retain bounded tails, never whole retained streams.
type Block struct {
	presentation.Value
	Annotation
	OutputSnapshot       *activitymodel.OutputView
	OutputContextLimited bool
}

// Annotation belongs only to terminal disclosure and layout, never feed records.
type Annotation struct {
	VerbAlign, VerbColumn, PathAlign, StatAlign, StatScale int
	TailRows, SourceRows, HiddenEdits                      int
	Flash, Collapsed, Hovered, Expanded                    bool
}

// Completed reasoning and successful output stay open until their agent's
// next standalone event, then collapses once events pause for
// OutputDebounce. Eligible outputs share the latest deadline across agents
// and late completions, so a quick run of commands collapses together rather than
// one row at a time. Restored history starts settled.
const OutputDebounce = 5 * time.Second

// Collapsible reports a settled block that can show as one row: finished
// thinking as its header, or a successful command's output as its
// line count.
func (b Block) Collapsible() bool {
	if b.UserShell {
		return false
	}
	if b.Hook != nil && (b.Hook.Status != "completed" || b.Hook.HasError) {
		return false
	}
	switch b.Kind {
	case "summary":
		return !b.Live
	case "op", "reads":
		lines := len(b.Tail) + b.TailOmitted
		return !b.Running && !b.Skipped && !b.Pending && b.ExitCode == 0 && lines > 1
	}
	return false
}

// GroupOperations applies shared semantic grouping and measures terminal columns.
func GroupOperations(blocks []Block) []Block {
	blocks = slices.Clone(blocks)
	values := make([]presentation.Value, len(blocks))
	for i, block := range blocks {
		values[i] = block.Value
	}
	values = presentation.GroupOperations(values)
	for i, value := range values {
		blocks[i].Value = value
	}
	measureGroups(blocks)
	return blocks
}

// collapseCapturedEdits bounds captured file lists without changing
// their retained evidence. Each elision opens one invocation's complete capture.
func collapseCapturedEdits(blocks []Block) []Block {
	values := make([]presentation.Value, len(blocks))
	for i, block := range blocks {
		values[i] = block.Value
	}
	var shown []Block
	for start := 0; start < len(blocks); {
		count, keep := presentation.FoldedEditRun(values[start:])
		end := start + max(1, count)
		keep = max(1, keep)
		shown = append(shown, blocks[start:start+keep]...)
		if hidden := end - start - keep; hidden > 0 {
			note := blocks[start+keep]
			note.HiddenEdits = hidden
			note.Label = "" // Empty edit path opens every retained file from this source.
			shown = append(shown, note)
		}
		start = end
	}
	measureGroups(shown)
	return shown
}

// widestAlignedVerb is the widest verb that pads to a shared column; wider
// verbs take their own width rather than widening their neighbors' column.
const widestAlignedVerb = 7

// AlignVerbs gives each run of adjacent operations one verb column, as wide as
// the widest verb in that run, so rows pad only to the verbs beside them.
func AlignVerbs(blocks []Block) []Block {
	blocks = slices.Clone(blocks)
	for start := 0; start < len(blocks); {
		end, column := start, 0
		for ; end < len(blocks) && slices.Contains([]string{"op", "reads", "filter"}, blocks[end].Kind); end++ {
			if w := ansi.StringWidth(RowVerb(blocks[end])); blocks[end].Kind != "filter" && w <= widestAlignedVerb {
				column = max(column, w+1)
			}
		}
		for k := start; k < end; k++ {
			blocks[k].VerbColumn = column
		}
		start = max(end, start+1)
	}
	return blocks
}

// RowVerb is the verb a row shows.
func RowVerb(b Block) string {
	if b.UserShell {
		if b.Running {
			return "You running"
		}
		return "You ran"
	}
	if b.Hook != nil {
		switch b.Hook.Status {
		case "running":
			if b.Running {
				return "Hook Running"
			}
		case "completed":
			return "Hook Ran"
		case "failed":
			return "Hook Failed"
		case "blocked":
			return "Hook Blocked"
		case "stopped":
			return "Hook Stopped"
		}
		return "Hook Run"
	}
	switch {
	case b.Pending:
		return "Pending"
	case b.Skipped && b.Verb == "Run":
		return "Skipped"
	case b.Kind == "op" && b.Verb == "Run" && b.Requested:
		return "Run"
	case b.Kind == "op" && b.Verb == "Run" && !b.Running && strings.HasPrefix(b.Approval, "Pending Approval"):
		return "Run"
	case b.Kind == "op" && b.Verb == "Run" && b.Running:
		return "Running"
	case b.Kind == "op" && b.Verb == "Run" && !b.Skipped:
		return "Ran"
	case b.committed():
		return "Committed"
	case b.EditOperation():
		return EditVerb(b)
	}
	return b.Verb
}

// EditVerb is a grouped row's verb: past tense once confirmed, and otherwise
// the requested action, whose status the group's first row names.
func EditVerb(b Block) string {
	if b.GroupHeader != "Edited" {
		return b.Verb
	}
	switch b.Verb {
	case "Create":
		return "Created"
	case "Delete":
		return "Deleted"
	case "Move":
		return "Moved"
	}
	return "Edited"
}

// EditStatus is the outcome a group's first row names after its source.
func EditStatus(b Block) string {
	_, status, _ := strings.Cut(b.GroupHeader, " ")
	return status
}

// measureGroups sets each edit group's shared verb, path, and count columns
// and its bar scale from the rows it currently holds.
func measureGroups(blocks []Block) {
	type group struct{ verb, path, stats, scale, rows int }
	groups := make(map[int]*group)
	starts := make([]int, len(blocks))
	start := -1
	for i, b := range blocks {
		switch {
		case b.GroupStart:
			start = i
		case b.GroupHeader == "":
			start = -1
		}
		starts[i] = -1
		blocks[i].VerbAlign, blocks[i].PathAlign, blocks[i].StatAlign, blocks[i].StatScale = 0, 0, 0, 0
		if start < 0 || !b.EditOperation() {
			continue
		}
		starts[i] = start
		g := groups[start]
		if g == nil {
			g = &group{}
			groups[start] = g
		}
		g.verb = max(g.verb, len(EditVerb(b)))
		path, added, removed, _, ok := EditStat(b.EditLabel())
		if !ok {
			continue
		}
		g.path = max(g.path, ansi.StringWidth(path))
		g.stats = max(g.stats, len(editCounts(added, removed)))
		g.scale = max(g.scale, added+removed)
		g.rows++
	}
	for i := range blocks {
		g := groups[starts[i]]
		if starts[i] < 0 || g == nil {
			continue
		}
		blocks[i].VerbAlign = g.verb
		// A lone file row has no column to share.
		if g.rows > 1 {
			blocks[i].PathAlign, blocks[i].StatAlign = g.path, g.stats
			// Bars compare confirmed changes only.
			if blocks[i].GroupHeader == "Edited" {
				blocks[i].StatScale = g.scale
			}
		}
	}
}

// editCounts is a row's line counts with zero counts omitted.
func editCounts(added, removed int) string {
	var counts []string
	if added > 0 {
		counts = append(counts, fmt.Sprintf("+%d", added))
	}
	if removed > 0 {
		counts = append(counts, fmt.Sprintf("-%d", removed))
	}
	return strings.Join(counts, " ")
}

// Receipts write "`path` +A -R"; app-server rows write "`path` · +A −R".
var editStatPattern = regexp.MustCompile(`^(?: ·)? \+(\d+) [-−](\d+)((?: · [^` + "`" + `]*)?)$`)

// EditStat reads a file row label as a path, line counts, and a trailing status.
func EditStat(label string) (path string, added, removed int, tail string, ok bool) {
	path, end, ok := markdownsyntax.CodeSpan(label, 0)
	if !ok {
		return "", 0, 0, "", false
	}
	match := editStatPattern.FindStringSubmatch(label[end:])
	if match == nil {
		return "", 0, 0, "", false
	}
	added, _ = strconv.Atoi(match[1])
	removed, _ = strconv.Atoi(match[2])
	return path, added, removed, match[3], true
}

// MergeTargets applies shared target grouping to terminal disclosure, retaining
// each original invocation for details and navigation.
func MergeTargets(blocks []Block) []Block {
	values := make([]presentation.Value, len(blocks))
	counts := make([]int, len(blocks))
	for i, b := range blocks {
		values[i] = b.Value
		if b.readContent() {
			counts[i] = b.TailOmitted + len(b.Tail)
		} else if len(b.Tail) > 0 || b.TailOmitted > 0 {
			counts[i] = -1
		}
	}
	var merged []Block
	for i := 0; i < len(blocks); {
		value, size := presentation.TargetRun(values[i:], counts[i:])
		first := blocks[i]
		first.Value = value
		for _, b := range blocks[i : i+size] {
			first.Flash = first.Flash || b.Flash
			first.Collapsed = first.Collapsed || b.Collapsed
		}
		merged = append(merged, first)
		i += size
	}
	return merged
}

// readContent reports collapsed content that one target's row can count.
func (b Block) readContent() bool {
	return b.Collapsed && b.ReadOutput() && len(b.Reads) == 1 && len(b.Tail) > 0
}
