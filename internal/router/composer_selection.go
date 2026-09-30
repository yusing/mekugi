package router

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// composerSelection is a screen selection mentioned as one composer token.
// The draft shows only its concise label; the quoted text travels in the
// submitted attachment envelope, so history, forks and resume keep it.
type composerSelection struct {
	start, end int
	text       string
	source     string // Optional literal origin, such as the diff file path.
}

const composerSelectionPrefix = "[Selected "

// composerSelectionLabel recognizes a bound selection placeholder. Transcript
// spans still require the host text element, not resemblance alone.
func composerSelectionLabel(label string) bool {
	return strings.HasPrefix(label, composerSelectionPrefix) && strings.HasSuffix(label, "]") && !strings.ContainsAny(label, "\n")
}

// insertSelection mentions text at the caret as one undoable token. A label
// already in the draft or pending input gains a counter, so the queued, steered
// or restored input that joins them still names one quote per mention.
func (u *appServerUI) insertSelection(description, text, source string) {
	if text == "" {
		return
	}
	if len(text) > fileAttachmentBudget/2 {
		u.setNotice(fmt.Sprintf("Selection exceeds the %d KiB mention limit; copy it instead", fileAttachmentBudget/2>>10), true)
		return
	}
	label := composerSelectionPrefix + description + "]"
	pending := slices.Concat(u.submission.parts, u.unsent, u.queued, u.steerParts(u.steers))
	used := func(label string) bool {
		return strings.Contains(u.draft, label) || slices.ContainsFunc(pending, func(d composerDraft) bool { return strings.Contains(d.text, label) })
	}
	for n := 2; used(label); n++ {
		label = fmt.Sprintf("%s%s %d]", composerSelectionPrefix, description, n)
	}
	u.run = runNone
	u.insertDraft(label)
	end := u.cursor()
	u.selections = append(u.selections, composerSelection{start: end - len(label), end: end, text: text, source: source})
	slices.SortFunc(u.selections, func(a, b composerSelection) int { return a.start - b.start })
	u.insertDraftText(" ")
}

// selectionFrames quotes one mentioned selection. Content splits like file
// frames; nothing is dropped silently.
func (d composerDraft) selectionFrames(selection composerSelection) []string {
	label := d.text[selection.start:selection.end]
	origin := ""
	if selection.source != "" {
		origin = fmt.Sprintf(" from %q", selection.source)
	}
	return frameAttachmentText(selection.text, func(start, end, total int) string {
		return fmt.Sprintf(selectionFramePrefix+"%q%s (UTF-8 bytes %d:%d of %d; quoted from the user's screen, not a separate request):\n", label, origin, start, end, total)
	})
}

// restoredSelections rebinds mentions from their submitted frames, so recalled
// input keeps its quotes after resume. Only a label that occurs once in the
// text binds; omitted content and ambiguous labels stay literal.
func restoredSelections(text string, frames []string) []composerSelection {
	var labels []string
	quotes := make(map[string]*composerSelection)
	for _, frame := range frames {
		rest, ok := strings.CutPrefix(frame, selectionFramePrefix)
		quoted, err := strconv.QuotedPrefix(rest)
		if !ok || err != nil {
			continue
		}
		label, _ := strconv.Unquote(quoted)
		rest = rest[len(quoted):]
		source := ""
		if after, ok := strings.CutPrefix(rest, " from "); ok {
			if quoted, err := strconv.QuotedPrefix(after); err == nil {
				source, _ = strconv.Unquote(quoted)
				rest = after[len(quoted):]
			}
		}
		header, body, ok := strings.Cut(rest, "\n")
		if !ok || !strings.HasPrefix(header, " (UTF-8 bytes ") || !composerSelectionLabel(label) {
			continue
		}
		if quotes[label] == nil {
			labels = append(labels, label)
			quotes[label] = &composerSelection{source: source}
		}
		quotes[label].text += body
	}
	var selections []composerSelection
	for _, label := range labels {
		if start := strings.Index(text, label); start >= 0 && strings.Count(text, label) == 1 {
			selection := *quotes[label]
			selection.start, selection.end = start, start+len(label)
			selections = append(selections, selection)
		}
	}
	slices.SortFunc(selections, func(a, b composerSelection) int { return a.start - b.start })
	return selections
}

// selectionFramePrefix starts every selection frame, including omissions.
const selectionFramePrefix = "Selected text for "

func selectionFrame(frame string) bool {
	return strings.HasPrefix(frame, selectionFramePrefix)
}
