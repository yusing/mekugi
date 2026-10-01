package tokenizer

import (
	"fmt"
	"strings"
)

// SelectRows admits LF-terminated rows in order, stopping at the first row
// that exceeds limit. Tail admission walks backwards but returns source order.
// Completed regexp pieces never change at a later row boundary. Only the piece
// crossing that boundary needs to be recounted, including newline/whitespace
// runs whose BPE counts need not grow monotonically.
func (c *codec) SelectRows(value string, limit int, tail bool) (string, error) {
	if limit < 1 || value != "" && !strings.HasSuffix(value, "\n") {
		return "", fmt.Errorf("row selection requires a positive budget and LF-terminated rows")
	}
	if len(value) <= limit {
		return value, nil
	}
	match, err := c.splitRegexp.FindStringMatch(value)
	if err != nil {
		return "", err
	}
	type piece struct {
		start, tokens int
	}
	var pieces []piece
	offset, completed, shown := 0, 0, 0
	for match != nil {
		text := match.String()
		if tail {
			pieces = append(pieces, piece{offset, c.countPiece(text)})
		} else {
			for from := 0; from < len(text); {
				end := strings.IndexByte(text[from:], '\n')
				if end < 0 {
					break
				}
				from += end + 1
				n, err := c.Count(text[:from])
				if err != nil {
					return "", err
				}
				if completed+n > limit {
					return value[:shown], nil
				}
				shown = offset + from
			}
			completed += c.countPiece(text)
			if completed > limit {
				return value[:shown], nil
			}
		}
		offset += len(text)
		match, err = c.splitRegexp.FindNextMatch(match)
		if err != nil {
			return "", err
		}
	}
	if !tail {
		return value[:shown], nil
	}
	index, following, start := len(pieces)-1, 0, len(value)
	for start > 0 {
		next := strings.LastIndexByte(value[:start-1], '\n') + 1
		for index >= 0 && next <= pieces[index].start {
			following += pieces[index].tokens
			index--
		}
		n := following
		if index >= 0 {
			// Cutting a newline/punctuation piece can let its remaining slash
			// or space join the following word's optional leading character.
			// Recount that following piece too so the suffix's lexer sees it.
			end := len(value)
			if index+2 < len(pieces) {
				end = pieces[index+2].start
			}
			if index+1 < len(pieces) {
				n -= pieces[index+1].tokens
			}
			partial, err := c.Count(value[next:end])
			if err != nil {
				return "", err
			}
			n += partial
		}
		if n > limit {
			break
		}
		start = next
	}
	return value[start:], nil
}

func (c *codec) countPiece(piece string) int {
	if _, ok := c.vocabulary[piece]; ok {
		return 1
	}
	if len(piece) > longPieceBytes {
		return len(c.mergeLongPiece(piece)) - 1
	}
	return len(c.mergePairs(piece)) - 1
}
