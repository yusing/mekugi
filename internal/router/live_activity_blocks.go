package router

import (
	"regexp"
	"slices"
	"strings"

	"github.com/yusing/mekugi/internal/livediff"
)

// The agents pane parses the router's own commentary grammar back into parts
// so it can lay them out natively. Unrecognized text stays a plain text block;
// nothing is interpreted, expanded, or executed.
type liveActivityBlock struct {
	kind     string // op, reads, message, start, error, text
	verb     string // Operation verb, or a message headline.
	label    string // Markdown remainder of the operation label.
	code     string // Inline code or fenced program under the label.
	lang     string
	fenced   bool
	from, to string
	body     string
	reads    []liveActivityRead
	journal  *liveActivityJournal // A final answer in journal-result form.
	compact  bool                 // Rendered in the clipped shared feed.
	exitCode int                  // Nonzero command exit; zero means no failure label.
}

// liveActivityJournal is a child's journal result laid out by the router's
// journal delivery grammar: answer groups, then this agent's recorded changes.
type liveActivityJournal struct {
	groups  []liveActivityAnswerGroup
	empty   bool
	changes string // Change ranges.
	stats   []liveActivityStat
	notes   []string // Unavailable or empty change reports.
	clipped bool
}

type liveActivityAnswerGroup struct {
	question string
	answers  []liveActivityAnswer
}

type liveActivityAnswer struct{ id, text string }

type liveActivityStat struct{ added, removed, path string }

type liveActivityRead struct {
	path   string
	ranges []string
}

// Read previews append line spans as space-separated N:M pairs.
var liveActivityReadRange = regexp.MustCompile(`^(.+?) (\d+:\d+(?: \d+:\d+)*)$`)

func parseLiveActivity(entry activityPaneEntry) []liveActivityBlock {
	text := livediff.Safe(entry.Text, false)
	// Only the router writes reply envelopes; child-authored text that looks
	// like one stays plain, so it cannot pose as another agent's message.
	if entry.Kind == "reply" {
		if from, to, headline, body, ok := parseLiveActivityEnvelope(text); ok {
			return []liveActivityBlock{{kind: "message", from: from, to: to, verb: headline, body: body}}
		}
	}
	switch entry.Kind {
	case "final":
		journal, _ := parseLiveActivityJournal(text)
		return []liveActivityBlock{{kind: "final", body: text, journal: journal}}
	case "start":
		if strings.HasPrefix(text, "Started") {
			return []liveActivityBlock{parseLiveActivityStart(text)}
		}
	case "error":
		return []liveActivityBlock{{kind: "error", body: text}}
	case "compaction":
		return []liveActivityBlock{{kind: "compaction", body: text}}
	case "tool":
		var blocks []liveActivityBlock
		for _, paragraph := range liveActivityParagraphs(text) {
			blocks = append(blocks, parseLiveActivityOperation(paragraph))
		}
		return mergeLiveActivityReads(blocks)
	}
	return []liveActivityBlock{{kind: "text", body: text}}
}

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

func parseLiveActivityEnvelope(text string) (from, to, headline, body string, ok bool) {
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

func parseLiveActivityStart(text string) liveActivityBlock {
	heading, body, _ := strings.Cut(text, "\n")
	_, details, _ := strings.Cut(heading, "Started · ")
	return liveActivityBlock{kind: "start", label: details, body: body}
}

// liveActivityParagraphs splits operations at blank lines outside fences.
func liveActivityParagraphs(text string) []string {
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
		if delimiter, ok := toolActivityFenceDelimiter(line); ok {
			fence = delimiter
		}
		current = append(current, line)
	}
	flush()
	return paragraphs
}

func parseLiveActivityOperation(paragraph string) liveActivityBlock {
	lines := strings.Split(paragraph, "\n")
	label := lines[0]
	cut := len(label)
	if i := strings.IndexByte(label, '`'); i >= 0 {
		cut = i
	}
	if i := strings.Index(label, " · "); i >= 0 && i < cut {
		cut = i
	}
	block := liveActivityBlock{kind: "op", verb: strings.TrimSuffix(strings.TrimSpace(label[:cut]), ":"), label: strings.TrimSpace(label[cut:])}
	if rest := lines[1:]; len(rest) > 0 {
		joined := strings.Join(rest, "\n")
		if delimiter, ok := toolActivityFenceDelimiter(rest[0]); ok {
			body := rest[1:]
			if n := len(body); n > 0 && body[n-1] == delimiter {
				body = body[:n-1]
			}
			block.fenced, block.lang, block.code = true, strings.TrimSpace(rest[0][len(delimiter):]), strings.Join(body, "\n")
		} else if code, end, ok := liveActivityCodeSpan(joined, 0); ok && end == len(joined) {
			block.code = code
		} else {
			block.body = joined
		}
	}
	if block.verb == "Read" && len(lines) == 1 {
		if reads, ok := parseLiveActivityReads(block.label); ok {
			block.kind, block.reads, block.label = "reads", reads, ""
		}
	}
	return block
}

func parseLiveActivityReads(label string) ([]liveActivityRead, bool) {
	var reads []liveActivityRead
	for i := 0; i < len(label); {
		code, end, ok := liveActivityCodeSpan(label, i)
		if !ok {
			return nil, false
		}
		read := liveActivityRead{path: code}
		if match := liveActivityReadRange.FindStringSubmatch(code); match != nil {
			read.path = match[1]
			read.ranges = strings.Fields(match[2])
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

// mergeLiveActivityReads collapses adjacent reads into one row, joining ranges
// of the same path. Merged blocks own their slices; parsed entries are shared.
func mergeLiveActivityReads(blocks []liveActivityBlock) []liveActivityBlock {
	var merged []liveActivityBlock
	for _, block := range blocks {
		n := len(merged)
		if block.kind != "reads" || n == 0 || merged[n-1].kind != "reads" {
			if block.kind == "reads" {
				block.reads = slices.Clone(block.reads)
				for i := range block.reads {
					block.reads[i].ranges = slices.Clone(block.reads[i].ranges)
				}
			}
			merged = append(merged, block)
			continue
		}
		last := &merged[n-1]
		for _, read := range block.reads {
			if i := slices.IndexFunc(last.reads, func(r liveActivityRead) bool { return r.path == read.path }); i >= 0 {
				last.reads[i].ranges = append(last.reads[i].ranges, read.ranges...)
			} else {
				last.reads = append(last.reads, liveActivityRead{path: read.path, ranges: slices.Clone(read.ranges)})
			}
		}
	}
	return merged
}

const liveActivityClippedAnswer = "… (full answer in Codex completion)"

// parseLiveActivityJournal reads the journal result grammar written by journal
// delivery. Any other shape stays authored Markdown.
func parseLiveActivityJournal(text string) (*liveActivityJournal, bool) {
	lines := strings.Split(text, "\n")
	if lines[0] != "Journal result" && !strings.HasPrefix(lines[0], "Journal result `") {
		return nil, false
	}
	journal := &liveActivityJournal{}
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
			journal.groups = append(journal.groups, liveActivityAnswerGroup{question: strings.Trim(strings.Join(question, "\n"), "\n")})
			current = len(journal.groups) - 1
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
				journal.groups = append(journal.groups, liveActivityAnswerGroup{})
				current = len(journal.groups) - 1
			}
			group := &journal.groups[current]
			group.answers = append(group.answers, liveActivityAnswer{id: id, text: strings.Trim(strings.Join(body, "\n"), "\n")})
		case strings.HasPrefix(line, "**Changes:**"):
			inChanges = true
			journal.changes = strings.TrimSpace(strings.TrimPrefix(line, "**Changes:**"))
		case inChanges && strings.HasPrefix(line, "    "):
			// Numstat columns are tabs, expanded by the sanitizer.
			if fields := strings.SplitN(strings.TrimPrefix(line, "    "), "    ", 3); len(fields) == 3 {
				journal.stats = append(journal.stats, liveActivityStat{fields[0], fields[1], fields[2]})
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
