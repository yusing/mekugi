package mekugi

import (
	"fmt"
	"strings"

	"github.com/yusing/mekugi/internal/verifiedrow"
)

// hashLine returns the lowercase four-digit verified-row hash for content.
func hashLine(content string) string {
	return verifiedrow.Hash(content)
}

// lineContent returns the logical-line content without its terminator.
func lineContent(text string, line logicalLine) string {
	return text[line.Start:line.ContentEnd]
}

// writeHashLine formats and writes one verified-row LINE:HASH reference line with display text.
func writeHashLine(output *strings.Builder, number int, content, displayed string) {
	fmt.Fprintf(output, "%d:%s %s\n", number, hashLine(content), displayed)
}
