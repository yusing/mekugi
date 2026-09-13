package verifiedrow

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
)

const HashLength = 4

var (
	ErrInvalidReference = errors.New("row must be LINE:HASH with a positive line and lowercase four-digit hash")
	ErrLineOutOfRange   = errors.New("row line is out of range")
)

// Reference is a parsed LINE:HASH verified-row identity.
type Reference struct {
	Line uint64
	Hash string
}

// Line identifies one logical line using UTF-8 byte offsets. End includes the
// line terminator, while ContentEnd excludes it.
type Line struct {
	Start      int
	ContentEnd int
	End        int
}

// Hash returns the lowercase four-digit verified-row hash for content.
func Hash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:HashLength/2])
}

// Hash16 returns the two hash bytes as one big-endian scalar for the WASM ABI.
func Hash16(content []byte) uint32 {
	sum := sha256.Sum256(content)
	return uint32(sum[0])<<8 | uint32(sum[1])
}

// Lines splits text into targetable logical lines. CR, LF, and CRLF terminate
// a line, and a final terminator does not create another empty line.
func Lines(text string) []Line {
	var lines []Line
	for start := 0; start < len(text); {
		line := lineAt(text, start)
		lines = append(lines, line)
		start = line.End
	}
	return lines
}

// Count returns the number of targetable logical lines in text.
func Count(text string) int {
	count := 0
	for start := 0; start < len(text); count++ {
		start = lineAt(text, start).End
	}
	return count
}

// At returns one positive one-based logical line without resolving stale row
// policy or allocating the complete line table.
func At(text string, number int) (Line, bool) {
	if number < 1 {
		return Line{}, false
	}
	current := 1
	for start := 0; start < len(text); current++ {
		line := lineAt(text, start)
		if current == number {
			return line, true
		}
		start = line.End
	}
	return Line{}, false
}

// lineAt returns one logical line starting at the given UTF-8 byte offset.
func lineAt(text string, start int) Line {
	contentEnd := start
	for contentEnd < len(text) && text[contentEnd] != '\r' && text[contentEnd] != '\n' {
		contentEnd++
	}
	end := contentEnd
	if end < len(text) {
		end++
		if text[contentEnd] == '\r' && end < len(text) && text[end] == '\n' {
			end++
		}
	}
	return Line{Start: start, ContentEnd: contentEnd, End: end}
}

// ParseReference parses the portable LINE:HASH framing without applying a
// caller's line-resolution policy.
func ParseReference(value string) (Reference, error) {
	lineText, hash, ok := strings.Cut(value, ":")
	if !ok || len(hash) != HashLength || lineText == "" || lineText[0] == '0' ||
		strings.Trim(lineText, "0123456789") != "" || strings.Trim(hash, "0123456789abcdef") != "" {
		return Reference{}, ErrInvalidReference
	}
	line, err := strconv.ParseUint(lineText, 10, 64)
	if err != nil {
		return Reference{}, ErrLineOutOfRange
	}
	return Reference{Line: line, Hash: hash}, nil
}
