package feed

import (
	"cmp"
	"regexp"
	"slices"
	"strings"

	markdownsyntax "github.com/yusing/mekugi/libraries/sourceanalysis/markdownsyntax"
)

// Source: internal/ui/activity/blocks.go:378:482,541:558@[3c06e4fa] classified source parsing.
// Read previews append line spans as space-separated N:M pairs.
var readRanges = regexp.MustCompile(`^(.+?) (\d+:\d+(?: \d+:\d+)*)$`)

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
			if markdownsyntax.ClosesFence(line, fence) {
				fence = ""
			}
			continue
		case strings.TrimSpace(line) == "":
			flush()
			continue
		}
		if delimiter, ok := markdownsyntax.FenceDelimiter(line); ok {
			fence = delimiter
		}
		current = append(current, line)
	}
	flush()
	return paragraphs
}

func ParseOperation(paragraph string) Content {
	lines := strings.Split(paragraph, "\n")
	label := lines[0]
	var timeouts []string
	for strings.HasSuffix(label, ")") {
		before, timeout, ok := strings.CutLast(label, " (timeout ")
		if !ok {
			break
		}
		timeouts = append([]string{timeout[:len(timeout)-1]}, timeouts...)
		label = before
	}
	cut := len(label)
	if i := strings.IndexByte(label, '`'); i >= 0 {
		cut = i
	}
	if i := strings.Index(label, " · "); i >= 0 && i < cut {
		cut = i
	}
	block := Content{Kind: "op", Verb: strings.TrimSuffix(strings.TrimSpace(label[:cut]), ":"), Label: strings.TrimSpace(label[cut:]), Timeouts: timeouts}
	if rest := lines[1:]; len(rest) > 0 {
		joined := strings.Join(rest, "\n")
		if delimiter, ok := markdownsyntax.FenceDelimiter(rest[0]); ok {
			body := rest[1:]
			if n := len(body); n > 0 && markdownsyntax.ClosesFence(body[n-1], delimiter) {
				body = body[:n-1]
			}
			block.Fenced, block.Lang, block.Code = true, strings.TrimSpace(strings.TrimLeft(rest[0], " ")[len(delimiter):]), strings.Join(body, "\n")
		} else if code, end, ok := markdownsyntax.CodeSpan(joined, 0); ok && end == len(joined) {
			block.Code = code
		} else {
			block.Body = joined
		}
	}
	if slices.Contains([]string{"Read", "View", "Inspect", "List", "Search", "Skill", "Create", "Edit", "Delete", "Move", "Write"}, block.Verb) && len(lines) == 1 && !(block.Verb == "Skill" && strings.HasPrefix(block.Label, "`run ")) {
		if reads, ok := parseReads(block.Label); ok {
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

func parseReads(label string) ([]Read, bool) {
	var reads []Read
	for i := 0; i < len(label); {
		code, end, ok := markdownsyntax.CodeSpan(label, i)
		if !ok {
			return nil, false
		}
		read := Read{Path: code}
		if match := readRanges.FindStringSubmatch(code); match != nil {
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

// Source: internal/router/live_activity_blocks.go:219:259@[3c06e4fa] receipt interpretation.
// Receipt summary of tool-managed files: count, sample paths, optional source.
var managedFiles = regexp.MustCompile(`^\+ (\d+) tool-managed files \((.*)\)(?: · (.+))?$`)

// ParseOperations interprets one invocation without merging its targets or
// attaching output, timing or execution outcomes.
func ParseOperations(text string) []Content {
	var blocks []Content
	previousSource := ""
	for _, paragraph := range Paragraphs(text) {
		block := ParseOperation(paragraph)
		if match := managedFiles.FindStringSubmatch(paragraph); match != nil {
			// A receipt's tool-managed files are confirmed edits of its source.
			source := cmp.Or(match[3], previousSource)
			block = Content{Kind: "op", Verb: "Edit", Label: match[1] + " tool-managed files (" + match[2] + ")"}
			if source != "" {
				block.Label += " · " + source
				block.EditSource, block.EditHeader = source, source != previousSource
			}
		}
		switch block.Verb {
		case "Create", "Edit", "Delete", "Move":
			if source, ok := strings.CutPrefix(block.Label, "· "); ok {
				// A targetless edit still belongs to its invocation, but must
				// retain the fact that its paths could not be resolved.
				block.Label = "paths unavailable · " + source
			}
			if _, source, ok := strings.CutLast(block.Label, " · "); ok {
				block.EditSource = source
				block.EditHeader = source != previousSource
			}
			if block.Fenced && block.Lang == "diff" {
				block.Code, block.Lang, block.Fenced = "", "", false
			}
		}
		previousSource = block.EditSource
		blocks = append(blocks, block)
	}
	return blocks
}
