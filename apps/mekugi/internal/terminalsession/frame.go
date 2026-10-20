package terminalsession

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/libraries/terminalui"
)

// Frame retains the last successfully painted cells and their source metadata.
// Clearing Rows invalidates incremental painting after an external editor.
type Frame struct {
	Notices   NoticeReceipt
	Source    SourceContext
	Rows      []string
	Copy      [][]terminalui.CopySpan
	Width     int
	Clipboard string
	composer  composerDocument
}

// SourceContext binds painted source to its original attachment and display.
type SourceContext struct {
	Attachment        uint64
	Thread, Workspace string
}

// CopyText queues OSC 52, which requests clipboard delivery without confirming it.
func (f *Frame) CopyText(text string) (notice string, warning bool) {
	if text == "" {
		return "Nothing to copy", true
	}
	f.Clipboard = "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\x07"
	return "Copy sent to terminal clipboard", false
}

// NoticeReceipt identifies the surfaces in one successfully delivered frame.
type NoticeReceipt struct {
	Attachment, Feedback uint64
	Composer, Content    bool
}

// Write publishes the frame and clears pending clipboard bytes only on success.
// Source: apps/mekugi/internal/router/native_shell.go:457:478@70e7ce50 terminalUI.paintNative
func (f *Frame) Write(out io.Writer, rendered RenderedFrame, width int, faint bool) error {
	f.Notices = NoticeReceipt{}
	rows, copied := rendered.Rows, rendered.Copy
	var b strings.Builder
	b.WriteString(f.Clipboard)
	b.WriteString("\x1b[?2026h\x1b[?25l\x1b[0m")
	for row, line := range rows {
		if f.Width == width && len(f.Rows) == len(rows) && line == f.Rows[row] {
			continue
		}
		fmt.Fprintf(&b, "\x1b[%d;1H\x1b[2K%s", row+1, line)
	}
	b.WriteString("\x1b[?2026l")
	frame := b.String()
	if !faint {
		frame = terminalui.FaintFallback(frame)
	}
	_, err := io.WriteString(out, frame)
	if err == nil {
		f.Notices = rendered.Notices
		f.Source = rendered.Source
		f.composer = rendered.composer
		f.Clipboard = ""
		f.Rows, f.Width, f.Copy = rows, width, copied
	}
	return err
}

// Screen snapshots painted rows as a terminal shows them and returns the link
// under the pointer. It does not retain the transient emulator.
// Source: apps/mekugi/internal/router/terminal_selection.go:556:567@70e7ce50 terminalUI.selectionScreen
func Screen(width, height int, source []string, x, y int) ([]string, string) {
	screen := vt.NewEmulator(width, height)
	defer screen.Close()
	for row, line := range source {
		fmt.Fprintf(screen, "\x1b[%d;1H%s", row+1, line)
	}
	link := ""
	if cell := screen.CellAt(x, y); cell != nil {
		link = cell.Link.URL
	}
	return strings.Split(screen.Render(), "\n"), link
}

// DiffScreen allocates its emulator only when the saved Diff pane is painted.
type DiffScreen struct{ *vt.Emulator }

// Paint keeps the saved pane's ANSI surface separate from final frame delivery.
func (s *DiffScreen) Paint(ctx context.Context, pane terminalui.Pane, width, height int) ([]string, error) {
	width, height = max(1, width), max(1, height)
	if s.Emulator == nil {
		s.Emulator = vt.NewEmulator(width, height)
		pane.Invalidate()
	} else if s.Width() != width || s.Height() != height {
		s.Resize(width, height)
		pane.Invalidate()
	}
	if err := pane.Paint(ctx, s, width, height); err != nil {
		return nil, err
	}
	return strings.Split(s.Render(), "\n"), nil
}

func (s *DiffScreen) Close() error {
	if s.Emulator == nil {
		return nil
	}
	err := s.Emulator.Close()
	s.Emulator = nil
	return err
}
