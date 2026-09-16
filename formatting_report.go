package mekugi

import (
	"fmt"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
)

// writeFormattingReferences reports only formatter effects, relative to the
// rendered edits, rather than repeating the author's patch.
func (w *workspace) writeFormattingReferences(report *strings.Builder, currentPath *string) {
	for _, file := range w.files {
		if file.deleted || file.editor.finalOffsets == nil {
			continue
		}
		before := file.editor.contentWithProjection(file.editor.renderedEdits())
		after := file.editor.content()
		writeReportFile(report, file.path, currentPath)
		writeFormattingReferences(report, before, after)
	}
}

func writeFormattingReferences(report *strings.Builder, before, after string) {
	if before == after {
		return
	}
	rows := func(source string) []string {
		lines := logicalLines(source)
		result := make([]string, len(lines))
		for i, line := range lines {
			result[i] = lineContent(source, line)
		}
		return result
	}
	a, b := rows(before), rows(after)
	report.WriteString("format (pre-format -> final)\n")
	for _, op := range difflib.NewMatcher(a, b).GetOpCodes() {
		switch {
		case op.Tag == 'e':
			if op.I1 != op.J1 {
				fmt.Fprintf(report, "shift %d-%d -> %d-%d (hashes unchanged)\n",
					op.I1+1, op.I2, op.J1+1, op.J2)
			}
		case op.I2-op.I1 == 1 && op.J2-op.J1 == 1:
			fmt.Fprintf(report, "%d:%s -> %d:%s\n",
				op.I1+1, hashLine(a[op.I1]), op.J1+1, hashLine(b[op.J1]))
		case op.J1 == op.J2:
			fmt.Fprintf(report, "Formatted removed %d-%d before final line %d\n", op.I1+1, op.I2, op.J1+1)
		default:
			fmt.Fprintf(report, "Formatted %d-%d\n", op.J1+1, op.J2)
			for i := op.J1; i < op.J2; i++ {
				writeHashLine(report, i+1, b[i], previewTextLimit(b[i], len(b[i])))
			}
		}
	}
}
