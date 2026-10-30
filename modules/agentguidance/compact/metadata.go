package compact

import "strings"

const CompactMetadataStart = "<mekugi-reload-v1>"
const CompactMetadataEnd = "</mekugi-reload-v1>"

// SplitCompactReload separates the terminal, line-start reload block from its
// handover without depending on the host's summary introduction.
func SplitCompactReload(text string) (handoff, metadata string, ok bool) {
	handoff, rest, found := strings.CutLast(text, "\n"+CompactMetadataStart)
	if !found {
		return text, "", false
	}
	metadata, ended := strings.CutSuffix(strings.TrimSpace(rest), CompactMetadataEnd)
	if !ended {
		return text, "", false
	}
	return handoff + "\n", metadata, true
}

// ReloadText projects reserved reload metadata out of answers. An unfinished
// block is hidden too; nonterminal examples remain ordinary text.
func ReloadText(text string) string {
	if body, _, ok := SplitCompactReload(text); ok {
		return body
	}
	if body, rest, ok := strings.CutLast(text, "\n"+CompactMetadataStart); ok && !strings.Contains(rest, CompactMetadataEnd) {
		return body + "\n"
	}
	if body, tail, ok := strings.CutLast(text, "\n"); ok && strings.HasPrefix(tail, "<mekugi-") && strings.HasPrefix(CompactMetadataStart, tail) {
		return body + "\n"
	}
	return text
}

// ReloadStream holds only a possible terminal reload block. Completed messages
// use ReloadText, which also restores quoted/nonterminal examples.
type ReloadStream struct {
	pending string
	hidden  bool
}

func (s *ReloadStream) Push(delta string) string {
	const marker = "\n" + CompactMetadataStart
	s.pending += delta
	if s.hidden {
		before, after, closed := strings.Cut(s.pending, CompactMetadataEnd)
		if !closed || strings.TrimSpace(after) == "" {
			return ""
		}
		s.pending, s.hidden = "", false
		return before + CompactMetadataEnd + s.Push(after)
	}
	if before, after, found := strings.Cut(s.pending, marker); found {
		s.pending, s.hidden = marker+after, true
		return before + s.Push("")
	}
	keep := min(len(s.pending), len(marker)-1)
	for keep > 0 && !strings.HasPrefix(marker, s.pending[len(s.pending)-keep:]) {
		keep--
	}
	visible := s.pending[:len(s.pending)-keep]
	s.pending = s.pending[len(s.pending)-keep:]
	return visible
}
