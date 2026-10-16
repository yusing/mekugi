package terminalview

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/modules/changehistory/presentation"
)

func (c *Pane) paintTo(t *testing.T, out io.Writer) {
	t.Helper()
	c.dirty = true
	if err := c.Paint(t.Context(), out, c.lastWidth, c.lastHeight); err != nil {
		t.Fatal(err)
	}
}

func liveDiffFrameRow(frame string, row int) string {
	start := strings.LastIndex(frame, fmt.Sprintf("\x1b[%d;1H\x1b[0m\x1b[2K", row))
	if start < 0 {
		return ""
	}
	text, _, _ := strings.Cut(frame[start:], fmt.Sprintf("\x1b[%d;1H", row+1))
	return ansi.Strip(text)
}

func liveDiffHighlightChunk(key, path, hunk string) presentation.Chunk {
	diff := "--- " + strconv.Quote(path) + "\n+++ " + strconv.Quote(path) + "\n" + hunk
	return presentation.Chunk{Key: key, Review: diffmodel.ReviewFile{BeforePath: path, AfterPath: path, Diff: diff}}
}
