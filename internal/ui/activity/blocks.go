package activity

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// The agents pane parses the router's own commentary grammar back into parts
// so it can lay them out natively. Unrecognized text stays a plain text block;
// nothing is interpreted, expanded, or executed.
type Block struct {
	Ended              time.Time     // Observed command end; zero when unavailable.
	Started            time.Time     // Observed live command start, presentation only.
	Duration           time.Duration // Host or measured segment duration; zero means unavailable.
	NotificationTiming bool          // Timestamps are UI notification observations, not host execution boundaries; Duration is host elapsed.
	Questions          []Question
	Source             uint64   // Activity entry identity for exact cross-pane navigation.
	Section            int      // Reasoning section ordinal within that entry, retained by dialogs.
	Kind               string   // op, reads, message, start, error, text
	Verb               string   // Operation verb, or a message headline.
	Label              string   // Markdown remainder of the operation label.
	Path               string   // Literal operation target, rendered with shared path styling.
	SyntaxPath         string   // File identity when Path is a compressed display label.
	Workdir            string   // Display path of a command directory other than the workspace; set on every row it applies to.
	ShowWorkdir        bool     // The invocation's first row labels Workdir.
	Timeouts           []string // Explicit execution bounds, as displayed.
	Code               string   // Inline code or fenced program under the label.
	Lang               string
	Fenced             bool
	From, To           string
	Owner              string
	Body               string
	Reads              []Read
	Journal            *Journal     // A final answer in journal-result form.
	WaitTargets        []WaitTarget // Canonical identity and observed status, not parsed display text.
	Results            *int
	ExitCode           int    // Nonzero command exit; zero means no failure label.
	Approval           string // Approval outcome, distinct from execution status and exit.
	BatchExit          bool   // This row owns an invocation-wide exit, not an individual operation's.
	EditSource         string // Editing source shared by this invocation's file rows.
	EditOutcome        string // Live segment outcome, distinct from captured file evidence.
	EditHeader         bool   // First row of a contiguous source group.
	GroupHeader        string // Presentation-only edit outcome shared by a group's rows.
	GroupStart         bool
	GroupCount         int  // Edit invocations in this group.
	VerbAlign          int  // Widest row verb in this row's group.
	VerbColumn         int  // Verb column shared with adjacent operations; 0 uses the default.
	PathAlign          int  // Widest aligned edit path in this row's group.
	StatAlign          int  // Widest line-count text in this row's group.
	StatScale          int  // Largest changed-line total in a multi-row group; 0 omits bars.
	Running            bool // A live command the host has not completed.
	Requested          bool // Source intent without observed per-command execution.
	Segment            bool // Ends one tracked segment of a command list; shows that segment's exit.
	Skipped            bool // A tracked segment the command list never reached.
	Tail               []string
	TailOmitted        int          // Output lines before Tail.
	TailRows           int          // Tail lines open output shows; 0 shows all of Tail.
	Changes            []ChangeRow  // Change history rows open output shows instead of Tail.
	Output             *Output      // The invocation's retained output, which the output dialog reads.
	Hook               *HookDetails // Hook metadata on an ordinary Run operation.
	// Members are merged read invocations or compact reasoning sections,
	// each retained as a separate output dialog page.
	Members    []Block
	SourceRows int    // Rows a command or program preview may use; 0 shows it whole.
	Flash      bool   // Presentation-only: another pane just opened this entry.
	Live       bool   // Reasoning still streaming.
	Elapsed    string // Formatted reasoning time, when observed from its first delta.
	// Collapsed shows a settled block as one row that opens it; Hovered
	// underlines that row under the pointer. Both are presentation-only.
	Collapsed, Hovered bool
	Expanded           bool // Explicit disclosure bypasses streaming reasoning's tail budget.
	// Rows paints styled presentation rows at a width; the dialog shows them
	// in place of Body's Markdown, and Body stays the copied text.
	Rows   func(width int) []string
	Detail string // Styled facts for the dialog's detail row when it has none of its own.
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
	if b.Hook != nil && (b.Hook.Status != "completed" || b.Hook.HasError) {
		return false
	}
	switch b.Kind {
	case "summary":
		return !b.Live
	case "op", "reads":
		lines := len(b.Tail) + b.TailOmitted
		return !b.Running && !b.Skipped && b.ExitCode == 0 && lines > 1
	}
	return false
}

// ReadOutput reports a file or skill read, whose output is what the agent
// read rather than a result to watch, so it starts collapsed.
func (b Block) ReadOutput() bool {
	return b.Verb == "Read" || b.Verb == "Skill" && b.Kind == "reads" ||
		b.Kind == "op" && (b.Verb == "Attached" || b.Verb == "Attached skill")
}

// MarkdownOutput reports skill content, rather than a skill program's output.
func (b Block) MarkdownOutput() bool {
	return (b.Verb == "Skill" || b.Verb == "Attached skill") && b.ReadOutput()
}

// Instant reports an operation that prints what it reads at once: a read,
// search, listing or inspection, not a program whose output is worth
// watching roll by.
func (b Block) Instant() bool {
	switch b.Verb {
	case "Read", "Search", "Inspect", "List":
		return true
	case "Skill":
		return b.Kind == "reads" // skills-mgr run runs a program.
	}
	return false
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
	if b.EditOutcome != "" {
		return "Edit " + b.EditOutcome
	}
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

// EditPath returns the unshortened target of an edit row, when known.
func EditPath(block Block) string {
	path, _, _ := liveActivityCodeSpan(block.Label, 0)
	return path
}

var directoryDeletionPattern = regexp.MustCompile(`^ • [0-9]+ files?(?: · .*)?$`)

// DirectoryDeletion reports a captured directory removal summary.
func (b Block) DirectoryDeletion() bool {
	_, end, ok := liveActivityCodeSpan(b.Label, 0)
	return b.Verb == "Delete" && ok && directoryDeletionPattern.MatchString(b.Label[end:])
}

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

// Journal is a child's journal result laid out by the router's
// journal delivery grammar: answer groups, then this agent's recorded changes.
type Journal struct {
	Groups  []AnswerGroup
	empty   bool
	Changes string // Change ranges.
	Stats   []Stat
	notes   []string // Capture diagnostics and unavailable or empty change reports.
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
	Lines  int // Collapsed content read from this target, once merged.
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
	var timeouts []string
	for strings.HasSuffix(label, ")") {
		at := strings.LastIndex(label, " (timeout ")
		if at < 0 {
			break
		}
		timeouts = append([]string{label[at+len(" (timeout ") : len(label)-1]}, timeouts...)
		label = label[:at]
	}
	cut := len(label)
	if i := strings.IndexByte(label, '`'); i >= 0 {
		cut = i
	}
	if i := strings.Index(label, " · "); i >= 0 && i < cut {
		cut = i
	}
	block := Block{Kind: "op", Verb: strings.TrimSuffix(strings.TrimSpace(label[:cut]), ":"), Label: strings.TrimSpace(label[cut:]), Timeouts: timeouts}
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

// MergeLiveActivityReads collapses adjacent targets of the same action, joining
// ranges of the same path. Reads whose content is collapsed join too, each
// target counting the lines read from it; a read still streaming, failed or
// left open stays its own row until it settles. Merged blocks own their
// slices; parsed entries are shared. Skill reads retain invocation output and
// timing in Members, with total duration on the grouped row.
func MergeLiveActivityReads(blocks []Block) []Block {
	var merged []Block
	var first Block // Only the last group can receive another adjacent read.
	for _, block := range blocks {
		n := len(merged)
		if n == 0 || !mergesReads(merged[n-1], block) {
			if block.Kind == "reads" {
				block.Reads = slices.Clone(block.Reads)
				for i := range block.Reads {
					block.Reads[i].Ranges = slices.Clone(block.Reads[i].Ranges)
				}
			}
			merged, first = append(merged, block), block
			continue
		}
		last := &merged[n-1]
		if last.Members == nil {
			last.Members = []Block{first}
			last.countContent()
		}
		last.Members = append(last.Members, block)
		if last.Verb == "Skill" {
			last.Duration += block.Duration
			last.Ended = block.Ended
		}
		last.Flash = last.Flash || block.Flash
		last.Collapsed = last.Collapsed || block.Collapsed
		block.countContent()
		for _, read := range block.Reads {
			if i := slices.IndexFunc(last.Reads, func(r Read) bool { return r.Path == read.Path }); i >= 0 {
				last.Reads[i].Ranges = append(last.Reads[i].Ranges, read.Ranges...)
				last.Reads[i].Lines += read.Lines
			} else {
				last.Reads = append(last.Reads, Read{Path: read.Path, Ranges: slices.Clone(read.Ranges), Lines: read.Lines})
			}
		}
	}
	return merged
}

// mergesReads reports whether next joins the read row last.
func mergesReads(last, next Block) bool {
	joins := func(b Block) bool {
		return b.Kind == "reads" && !b.Running && !b.Skipped && b.Approval == "" && b.Results == nil && b.ExitCode == 0 &&
			(b.Verb == "Skill" || b.Started.IsZero() && b.Duration == 0) &&
			(len(b.Tail) == 0 && b.TailOmitted == 0 || b.readContent())
	}
	return last.Verb == next.Verb && last.Workdir == next.Workdir && joins(last) && joins(next)
}

// AddTimeouts carries presentation metadata outside command and operand spans.
func AddTimeouts(text string, timeouts []string) string {
	paragraphs := Paragraphs(text)
	for i, paragraph := range paragraphs {
		known := ParseOperation(paragraph).Timeouts
		heading, body, multiline := strings.Cut(paragraph, "\n")
		for _, timeout := range timeouts {
			if !slices.Contains(known, timeout) {
				heading += " (timeout " + timeout + ")"
				known = append(known, timeout)
			}
		}
		paragraphs[i] = heading
		if multiline {
			paragraphs[i] += "\n" + body
		}
	}
	return strings.Join(paragraphs, "\n\n")
}

// readContent reports collapsed content that one target's row can count.
func (b Block) readContent() bool {
	return b.Collapsed && b.ReadOutput() && len(b.Reads) == 1 && len(b.Tail) > 0
}

// countContent moves a read's collapsed content count onto its target.
func (b *Block) countContent() {
	if b.readContent() {
		b.Reads = slices.Clone(b.Reads)
		b.Reads[0].Lines = b.TailOmitted + len(b.Tail)
		b.Tail, b.TailOmitted = nil, 0
	}
}

const liveActivityClippedAnswer = "… (full answer in Codex completion)"

// ParseJournal reads the child result grammar written by journal delivery:
// the answer, then the change report that always closes a child result.
// Results retained from before the heading was dropped still lead with
// "Journal result". Any other shape stays authored Markdown.
func ParseJournal(text string) (*Journal, bool) {
	first, body, _ := strings.Cut(text, "\n")
	headed := first == "Journal result" || strings.HasPrefix(first, "Journal result `")
	if !headed {
		body = text
	}
	lines := strings.Split(body, "\n")
	report := len(lines)
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(lines[i], "**Changes:**") {
			report = i
			break
		}
	}
	if report == len(lines) && !headed {
		return nil, false
	}
	journal := &Journal{}
	if !parseJournalChanges(journal, lines[report:], headed) {
		return nil, false
	}
	// Only headed results ever used the v1 item grammar; a headless answer of
	// code-span bullets is Markdown.
	answer := lines[:report]
	if !headed || !parseJournalAnswers(journal, answer) {
		// A tree journal's result is the answer as Markdown, then its task
		// and note rows as bullets.
		text := strings.Trim(strings.Join(answer, "\n"), "\n")
		switch text {
		case "", "No new journal entries.", "No journal entries.":
			journal.empty = true
		default:
			journal.Groups = []AnswerGroup{{Answers: []Answer{{Text: text}}}}
		}
	}
	return journal, true
}

// parseJournalChanges reads the change report, rejecting lines it never
// writes so an authored answer that merely quotes the heading stays Markdown.
// The report always follows its heading with a note or evaluation rows.
func parseJournalChanges(journal *Journal, lines []string, headed bool) bool {
	reported, numstat := headed, false
	for i, line := range lines {
		switch {
		case i == 0:
			journal.Changes = strings.TrimSpace(strings.TrimPrefix(line, "**Changes:**"))
		case line == "":
		case strings.HasPrefix(line, "Recorded evaluations"), strings.HasPrefix(line, "Aggregated numstat"):
			reported, numstat = true, true
		case strings.HasPrefix(line, "    ") && (numstat || headed):
			// Numstat columns are tabs, expanded by the sanitizer.
			if fields := strings.SplitN(strings.TrimPrefix(line, "    "), "    ", 4); len(fields) == 4 {
				switch {
				case fields[0] == "?":
					journal.notes = append(journal.notes, "Capture incomplete: "+fields[3])
				case fields[1] == "-" || fields[2] == "-":
					journal.notes = append(journal.notes, fields[0]+" "+fields[3]+" · counts unavailable")
				default:
					journal.Stats = append(journal.Stats, Stat{fields[0], fields[1], fields[2], fields[3]})
				}
			} else {
				journal.notes = append(journal.notes, strings.TrimSpace(line))
			}
		case line == "No recorded changes.", line == "No recorded file changes.", strings.HasPrefix(line, "Changes unavailable: "),
			strings.HasPrefix(line, "Stat unavailable: "), strings.HasPrefix(line, "Cumulative: "):
			reported = true
			journal.notes = append(journal.notes, line)
		default:
			return false
		}
	}
	return reported
}

// parseJournalAnswers reads the item grammar of v1 journal results: answers
// under their questions, each headed by its item ID.
func parseJournalAnswers(journal *Journal, lines []string) bool {
	current := -1
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		switch {
		case line == liveActivityClippedAnswer:
			journal.clipped = true
		case line == "":
		case line == "---":
			current = -1
		case line == "No journal entries.":
			journal.empty = true
		case line == "**Question:**":
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
		case strings.HasPrefix(line, "- `"):
			id, end, ok := liveActivityCodeSpan(line, 2)
			if !ok || end != len(line) {
				journal.Groups, journal.empty, journal.clipped = nil, false, false
				return false
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
		default:
			journal.Groups, journal.empty, journal.clipped = nil, false, false
			return false
		}
	}
	return true
}
