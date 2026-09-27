package activity

import (
	"regexp"
	"slices"
	"strings"
)

// The agents pane parses the router's own commentary grammar back into parts
// so it can lay them out natively. Unrecognized text stays a plain text block;
// nothing is interpreted, expanded, or executed.
type Block struct {
	Source         uint64 // Activity entry identity for exact cross-pane navigation.
	Kind           string // op, reads, message, start, error, text
	Verb           string // Operation verb, or a message headline.
	Label          string // Markdown remainder of the operation label.
	Code           string // Inline code or fenced program under the label.
	Lang           string
	Fenced         bool
	From, To       string
	Owner          string
	Body           string
	Reads          []Read
	Journal        *Journal // A final answer in journal-result form.
	Results        *int
	ExitCode       int    // Nonzero command exit; zero means no failure label.
	EditSource     string // Editing source shared by this invocation's file rows.
	EditHeader     bool   // First row of a contiguous source group.
	GroupHeader    string // Presentation-only operation group.
	GroupStart     bool
	GroupReasoning string // Adjacent single-line reasoning carried by this heading.
	GroupSummary   bool   // The original reasoning row is rendered in the next heading.
}

// GroupOperations adds Codex-style headings without combining invocation identities.
// EditHeader comes from the entry parser; exploration may span adjacent entries.
func GroupOperations(blocks []Block) []Block {
	blocks = slices.Clone(blocks)
	previous := ""
	for i := range blocks {
		b := &blocks[i]
		b.GroupReasoning, b.GroupSummary = "", false
		if b.Kind == "filter" {
			// Attached output annotations belong to the preceding operation.
			b.GroupHeader, b.GroupStart = previous, false
			continue
		}
		heading := ""
		if b.EditSource != "" {
			heading = b.EditSource
		} else if slices.Contains([]string{"Read", "Search", "List", "Inspect"}, b.Verb) && (b.Kind == "op" || b.Kind == "reads") {
			heading = "Explored"
		}
		b.GroupHeader = heading
		b.GroupStart = heading != "" && (heading != previous || b.EditHeader)
		previous = heading
	}
	for i := 1; i < len(blocks); i++ {
		previous, current := &blocks[i-1], &blocks[i]
		if previous.Kind != "summary" || !current.GroupStart {
			continue
		}
		body := ReasoningSummaryBody(previous.Body)
		if body != "" && !strings.ContainsAny(body, "\r\n") {
			previous.GroupSummary = true
			current.GroupReasoning = body
		}
	}
	return blocks
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

type Stat struct{ Added, Removed, Path string }

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
			if fields := strings.SplitN(strings.TrimPrefix(line, "    "), "    ", 3); len(fields) == 3 {
				journal.Stats = append(journal.Stats, Stat{fields[0], fields[1], fields[2]})
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
