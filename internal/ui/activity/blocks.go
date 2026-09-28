package activity

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// The agents pane parses the router's own commentary grammar back into parts
// so it can lay them out natively. Unrecognized text stays a plain text block;
// nothing is interpreted, expanded, or executed.
type Block struct {
	Source      uint64 // Activity entry identity for exact cross-pane navigation.
	Kind        string // op, reads, message, start, error, text
	Verb        string // Operation verb, or a message headline.
	Label       string // Markdown remainder of the operation label.
	Code        string // Inline code or fenced program under the label.
	Lang        string
	Fenced      bool
	From, To    string
	Owner       string
	Body        string
	Reads       []Read
	Journal     *Journal // A final answer in journal-result form.
	Results     *int
	ExitCode    int    // Nonzero command exit; zero means no failure label.
	EditSource  string // Editing source shared by this invocation's file rows.
	EditHeader  bool   // First row of a contiguous source group.
	GroupHeader string // Presentation-only edit outcome shared by a group's rows.
	GroupStart  bool
	GroupCount  int // Edit invocations in this group.
	VerbAlign   int // Widest row verb in this row's group.
	VerbColumn  int // Verb column shared with adjacent operations; 0 uses the default.
	PathAlign   int // Widest aligned edit path in this row's group.
	StatAlign   int // Widest line-count text in this row's group.
	StatScale   int // Largest changed-line total in a multi-row group; 0 omits bars.
	Tail        []string
	TailOmitted int  // Output lines before Tail.
	Flash       bool // Presentation-only: another pane just opened this entry.
}

// GroupOperations groups adjacent edits from one source and outcome, across
// invocations, so their rows share one verb cell. Invocation identities stay
// on their rows. Other operations stand alone.
func GroupOperations(blocks []Block) []Block {
	blocks = slices.Clone(blocks)
	previous, previousSource, start := "", "", -1
	for i := range blocks {
		b := &blocks[i]
		b.GroupCount = 0
		if b.Kind == "filter" {
			// Attached output annotations belong to the preceding operation.
			b.GroupHeader, b.GroupStart = previous, false
			continue
		}
		heading := ""
		if b.EditSource != "" {
			heading = editHeading(*b)
		}
		b.GroupHeader = heading
		b.GroupStart = heading != "" && (heading != previous || b.EditSource != previousSource)
		previous, previousSource = heading, b.EditSource
		if b.GroupStart {
			start = i
		}
		if b.EditSource != "" && (b.EditHeader || b.GroupStart) {
			blocks[start].GroupCount++
		}
	}
	measureGroups(blocks)
	return blocks
}

const requestedEdit = " (requested)"

// editHeading keeps intent and failures from reading as completed edits.
func editHeading(b Block) string {
	if strings.HasSuffix(b.EditSource, requestedEdit) {
		return "Edit requested"
	}
	label := editLabel(b)
	for _, status := range []string{"failed", "declined", "pending"} {
		if strings.HasSuffix(label, " · "+status) {
			return "Edit " + status
		}
	}
	return "Edited"
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
	switch {
	case b.Kind == "op" && b.Verb == "Run":
		return "Ran"
	case b.GroupHeader != "":
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
		if start < 0 || b.Kind != "op" {
			continue
		}
		starts[i] = start
		g := groups[start]
		if g == nil {
			g = &group{}
			groups[start] = g
		}
		g.verb = max(g.verb, len(EditVerb(b)))
		path, added, removed, _, ok := EditStat(editLabel(b))
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

// editLabel is a file row's label without the source its heading names.
func editLabel(b Block) string {
	if b.EditSource == "" {
		return b.Label
	}
	return strings.TrimSuffix(b.Label, " · "+b.EditSource)
}

// Receipts write "`path` +A -R"; app-server rows write "`path` · +A −R".
var editStatPattern = regexp.MustCompile(`^(?: ·)? \+(\d+) [-−](\d+)((?: · [^` + "`" + `]*)?)$`)

// EditStat reads a file row label as a path, line counts, and a trailing status.
func EditStat(label string) (path string, added, removed int, tail string, ok bool) {
	path, end, ok := liveActivityCodeSpan(label, 0)
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

// MergeEdits folds repeated edits of one path under one Edited heading into a
// single row with summed counts. It drops rows, so only views that do not
// navigate by row identity use it.
func MergeEdits(blocks []Block) []Block {
	var merged []Block
	rows := make(map[string]int)
	for _, block := range blocks {
		if block.GroupStart || block.GroupHeader != "Edited" || block.Verb == "Delete" || block.Verb == "Move" {
			// A later row after a delete or move describes a new file state.
			clear(rows)
		}
		path, added, removed, tail, ok := EditStat(editLabel(block))
		mergeable := ok && block.GroupHeader == "Edited" && block.Kind == "op" && block.ExitCode == 0 && tail == ""
		if !mergeable {
			merged = append(merged, block)
			continue
		}
		if k, found := rows[path]; found && (block.Verb == "Edit" || block.Verb == merged[k].Verb) {
			last := &merged[k]
			_, a, r, _, _ := EditStat(editLabel(*last))
			last.Label = fmt.Sprintf("%s +%d -%d · %s", codeSpan(path), a+added, r+removed, last.EditSource)
			continue
		}
		if block.Verb == "Edit" || block.Verb == "Create" {
			rows[path] = len(merged)
		}
		merged = append(merged, block)
	}
	measureGroups(merged)
	return merged
}

// codeSpan encloses text in a Markdown code span that liveActivityCodeSpan reads back.
func codeSpan(text string) string {
	run, longest := 0, 0
	for _, r := range text {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", longest+1)
	if longest > 0 || strings.HasPrefix(text, " ") && strings.HasSuffix(text, " ") && len(text) > 1 {
		return fence + " " + text + " " + fence
	}
	return fence + text + fence
}

// Journal is a child's journal result laid out by the router's
// journal delivery grammar: answer groups, then this agent's recorded changes.
type Journal struct {
	Groups  []AnswerGroup
	empty   bool
	Changes string // Change ranges.
	Stats   []Stat
	notes   []string // Unavailable or empty change reports.
	clipped bool
}

type AnswerGroup struct {
	Question string
	Answers  []Answer
	Target   uint64
}

type Answer struct{ ID, Text string }

type Stat struct{ Status, Added, Removed, Path string }

type Read struct {
	Path   string
	Ranges []string
}

// Read previews append line spans as space-separated N:M pairs.
var liveActivityReadRange = regexp.MustCompile(`^(.+?) (\d+:\d+(?: \d+:\d+)*)$`)

// liveActivityCodeSpan reads one Markdown code span starting at i.
func liveActivityCodeSpan(s string, i int) (string, int, bool) {
	if i >= len(s) || s[i] != '`' {
		return "", i, false
	}
	run := 1
	for i+run < len(s) && s[i+run] == '`' {
		run++
	}
	end := strings.Index(s[i+run:], strings.Repeat("`", run))
	if end < 0 {
		return "", i, false
	}
	code := s[i+run : i+run+end]
	if len(code) > 1 && code[0] == ' ' && code[len(code)-1] == ' ' {
		code = code[1 : len(code)-1]
	}
	return code, i + run + end + run, true
}

func ParseEnvelope(text string) (from, to, headline, body string, ok bool) {
	if !strings.HasPrefix(text, "[") {
		return
	}
	from, i, ok := liveActivityCodeSpan(text, 1)
	if !ok || !strings.HasPrefix(text[i:], " -> ") {
		return "", "", "", "", false
	}
	to, j, ok := liveActivityCodeSpan(text, i+4)
	if !ok || !strings.HasPrefix(text[j:], "]") {
		return "", "", "", "", false
	}
	headline, body, _ = strings.Cut(strings.TrimPrefix(text[j+1:], " "), "\n")
	return from, to, headline, strings.Trim(body, "\n"), true
}

func ParseStart(text string) Block {
	heading, body, _ := strings.Cut(text, "\n")
	_, details, _ := strings.Cut(heading, "Started · ")
	body = strings.TrimPrefix(strings.TrimLeft(body, "\n"), "Spawn assignment:\n")
	return Block{Kind: "start", Label: details, Body: body}
}

// Paragraphs splits operations at blank lines outside fences.
func Paragraphs(text string) []string {
	var paragraphs, current []string
	flush := func() {
		if len(current) > 0 {
			paragraphs = append(paragraphs, strings.Join(current, "\n"))
			current = nil
		}
	}
	fence := ""
	for line := range strings.SplitSeq(text, "\n") {
		switch {
		case fence != "":
			current = append(current, line)
			if line == fence {
				fence = ""
			}
			continue
		case strings.TrimSpace(line) == "":
			flush()
			continue
		}
		if delimiter, ok := FenceDelimiter(line); ok {
			fence = delimiter
		}
		current = append(current, line)
	}
	flush()
	return paragraphs
}

func ParseOperation(paragraph string) Block {
	lines := strings.Split(paragraph, "\n")
	label := lines[0]
	cut := len(label)
	if i := strings.IndexByte(label, '`'); i >= 0 {
		cut = i
	}
	if i := strings.Index(label, " · "); i >= 0 && i < cut {
		cut = i
	}
	block := Block{Kind: "op", Verb: strings.TrimSuffix(strings.TrimSpace(label[:cut]), ":"), Label: strings.TrimSpace(label[cut:])}
	if rest := lines[1:]; len(rest) > 0 {
		joined := strings.Join(rest, "\n")
		if delimiter, ok := FenceDelimiter(rest[0]); ok {
			body := rest[1:]
			if n := len(body); n > 0 && body[n-1] == delimiter {
				body = body[:n-1]
			}
			block.Fenced, block.Lang, block.Code = true, strings.TrimSpace(rest[0][len(delimiter):]), strings.Join(body, "\n")
		} else if code, end, ok := liveActivityCodeSpan(joined, 0); ok && end == len(joined) {
			block.Code = code
		} else {
			block.Body = joined
		}
	}
	if slices.Contains([]string{"Read", "View", "Inspect", "List", "Search", "Skill", "Create", "Edit", "Delete", "Move", "Write"}, block.Verb) && len(lines) == 1 && !(block.Verb == "Skill" && strings.HasPrefix(block.Label, "`run ")) {
		if reads, ok := parseLiveActivityReads(block.Label); ok {
			if block.Verb != "Read" {
				for i := range reads {
					if len(reads[i].Ranges) > 0 {
						reads[i].Path += " " + strings.Join(reads[i].Ranges, " ")
						reads[i].Ranges = nil
					}
				}
			}
			block.Kind, block.Reads, block.Label = "reads", reads, ""
		}
	}
	return block
}

func parseLiveActivityReads(label string) ([]Read, bool) {
	var reads []Read
	for i := 0; i < len(label); {
		code, end, ok := liveActivityCodeSpan(label, i)
		if !ok {
			return nil, false
		}
		read := Read{Path: code}
		if match := liveActivityReadRange.FindStringSubmatch(code); match != nil {
			read.Path = match[1]
			read.Ranges = strings.Fields(match[2])
		}
		reads = append(reads, read)
		if i = end; i < len(label) {
			if label[i] != ' ' {
				return nil, false
			}
			i++
		}
	}
	return reads, len(reads) > 0
}

// mergeLiveActivityReads collapses adjacent targets of the same action, joining ranges
// of the same path. Merged blocks own their slices; parsed entries are shared.
func MergeLiveActivityReads(blocks []Block) []Block {
	var merged []Block
	for _, block := range blocks {
		n := len(merged)
		if block.Kind != "reads" || n == 0 || merged[n-1].Kind != "reads" || merged[n-1].Verb != block.Verb || block.Results != nil || merged[n-1].Results != nil {
			if block.Kind == "reads" {
				block.Reads = slices.Clone(block.Reads)
				for i := range block.Reads {
					block.Reads[i].Ranges = slices.Clone(block.Reads[i].Ranges)
				}
			}
			merged = append(merged, block)
			continue
		}
		last := &merged[n-1]
		for _, read := range block.Reads {
			if i := slices.IndexFunc(last.Reads, func(r Read) bool { return r.Path == read.Path }); i >= 0 {
				last.Reads[i].Ranges = append(last.Reads[i].Ranges, read.Ranges...)
			} else {
				last.Reads = append(last.Reads, Read{Path: read.Path, Ranges: slices.Clone(read.Ranges)})
			}
		}
	}
	return merged
}

const liveActivityClippedAnswer = "… (full answer in Codex completion)"

// parseLiveActivityJournal reads the journal result grammar written by journal
// delivery. Any other shape stays authored Markdown.
func ParseJournal(text string) (*Journal, bool) {
	lines := strings.Split(text, "\n")
	if lines[0] != "Journal result" && !strings.HasPrefix(lines[0], "Journal result `") {
		return nil, false
	}
	journal := &Journal{}
	current, inChanges := -1, false
	for i := 1; i < len(lines); i++ {
		line := lines[i]
		switch {
		case line == liveActivityClippedAnswer:
			journal.clipped = true
		case line == "":
		case line == "---":
			current = -1
		case line == "No journal entries." && !inChanges:
			journal.empty = true
		case line == "**Question:**" && !inChanges:
			var question []string
			for i+1 < len(lines) && lines[i+1] != "**Answer:**" && lines[i+1] != "**Answers:**" && lines[i+1] != liveActivityClippedAnswer {
				i++
				question = append(question, lines[i])
			}
			if i+1 < len(lines) && lines[i+1] != liveActivityClippedAnswer {
				i++ // The answer label.
			}
			journal.Groups = append(journal.Groups, AnswerGroup{Question: strings.Trim(strings.Join(question, "\n"), "\n")})
			current = len(journal.Groups) - 1
		case strings.HasPrefix(line, "- `") && !inChanges:
			id, end, ok := liveActivityCodeSpan(line, 2)
			if !ok || end != len(line) {
				return nil, false
			}
			var body []string
			for i+1 < len(lines) && (lines[i+1] == "" || strings.HasPrefix(lines[i+1], "  ")) {
				i++
				body = append(body, strings.TrimPrefix(lines[i], "  "))
			}
			if current < 0 {
				journal.Groups = append(journal.Groups, AnswerGroup{})
				current = len(journal.Groups) - 1
			}
			group := &journal.Groups[current]
			group.Answers = append(group.Answers, Answer{ID: id, Text: strings.Trim(strings.Join(body, "\n"), "\n")})
		case strings.HasPrefix(line, "**Changes:**"):
			inChanges = true
			journal.Changes = strings.TrimSpace(strings.TrimPrefix(line, "**Changes:**"))
		case inChanges && strings.HasPrefix(line, "    "):
			// Numstat columns are tabs, expanded by the sanitizer.
			if fields := strings.SplitN(strings.TrimPrefix(line, "    "), "    ", 4); len(fields) == 4 {
				journal.Stats = append(journal.Stats, Stat{fields[0], fields[1], fields[2], fields[3]})
			}
		case inChanges && strings.HasPrefix(line, "Aggregated numstat"):
		case inChanges:
			journal.notes = append(journal.notes, line)
		default:
			return nil, false
		}
	}
	return journal, true
}
