package router

import (
	"fmt"
	"io"
 "os"
 "golang.org/x/sys/unix"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

func (u *terminalUI) configureCodex(host io.Writer) {
	u.mouseModes = make(map[ansi.Mode]bool)
	u.codex.SetCallbacks(vt.Callbacks{
		CursorVisibility: func(visible bool) { u.cursorVisible = visible },
		AltScreen:        func(bool) { u.codexScroll = 0 },
		EnableMode: func(mode ansi.Mode) {
			if codexMouseMode(mode) {
				u.mouseModes[mode] = true
			}
		},
		DisableMode: func(mode ansi.Mode) { delete(u.mouseModes, mode) },
	})
	// These requests target the host terminal, not just the embedded screen.
	// In particular, outer terminal managers use Codex titles/progress for status.
	// The VT parser assembles fragmented sequences before forwarding them once.
	for _, command := range []int{0, 2, 9, 52} {
		u.codex.RegisterOscHandler(command, func(data []byte) bool {
			if u.hostError == nil {
				_, u.hostError = fmt.Fprintf(host, "\x1b]%s\x1b\\", data)
			}
			// Keep the emulator's own title handling too.
			return command != 0 && command != 2
		})
	}
}

func codexMouseMode(mode ansi.Mode) bool {
	switch mode {
	case ansi.ModeMouseX10, ansi.ModeMouseNormal, ansi.ModeMouseHighlight, ansi.ModeMouseButtonEvent, ansi.ModeMouseAnyEvent:
		return true
	}
	return false
}

// Main-screen scrollback stays local unless Codex has requested mouse events.
// Prefix PageUp/PageDown remains available when an application owns the mouse.
func (u *terminalUI) scrollCodex(delta int) {
	if !u.codex.IsAltScreen() {
		u.codexScroll = min(max(0, u.codexScroll+delta), u.codex.ScrollbackLen())
		u.focus = 0
	}
}

func (u *terminalUI) codexFrame() string {
	if u.codexScroll == 0 || u.codex.IsAltScreen() {
		return u.codex.Render()
	}
	history := u.codex.ScrollbackLen()
	u.codexScroll = min(u.codexScroll, history)
	start := history - u.codexScroll
	buffer := uv.NewBuffer(u.codex.Width(), u.codex.Height())
	for y := range u.codex.Height() {
		for x := range u.codex.Width() {
			var cell *uv.Cell
			if start+y < history {
				cell = u.codex.ScrollbackCellAt(x, start+y)
			} else {
				cell = u.codex.CellAt(x, start+y-history)
			}
			buffer.SetCell(x, y, cell)
		}
	}
	return buffer.Render()
}

// Preserve the master's nonblocking flag; pty.Setsize calls File.Fd, which
// switches a Go-managed descriptor back to blocking I/O.
func resizeTerminalPTY(file *os.File,width,height int) error {
 raw,err:=file.SyscallConn();if err!=nil{return err}
 var resizeErr error
 if err=raw.Control(func(fd uintptr){resizeErr=unix.IoctlSetWinsize(int(fd),unix.TIOCSWINSZ,&unix.Winsize{Col:uint16(width),Row:uint16(height)})});err!=nil{return err}
 return resizeErr
}
