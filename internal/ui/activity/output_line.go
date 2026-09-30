package activity

// outputLineBuffer assembles bounded streamed rows. Consumers own completed
// line retention and reset the line in endLine.
type outputLineBuffer struct {
	line []byte
	cr   bool
}

func (b *outputLineBuffer) write(output string, limit int, endLine func()) {
	for i := range len(output) {
		c := output[i]
		if b.cr && c != '\n' {
			b.line = b.line[:0]
		}
		b.cr = false
		switch {
		case c == '\r':
			b.cr = true
		case c == '\n':
			endLine()
		case len(b.line) <= limit:
			b.line = append(b.line, c)
		}
	}
}
