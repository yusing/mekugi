package logicalrow

// Line identifies one logical line using UTF-8 byte offsets. End includes the
// line terminator, while ContentEnd excludes it.
type Line struct {
	Start      int
	ContentEnd int
	End        int
}

// Count returns the number of targetable logical lines in text.
func Count(text string) int {
	count := 0
	for start := 0; start < len(text); count++ {
		start = lineAt(text, start).End
	}
	return count
}

// At returns one positive one-based logical line without allocating the complete line table.
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
