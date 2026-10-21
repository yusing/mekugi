package terminalsession

// Rect uses zero-based terminal cells.
type Rect struct{ X, Y, W, H int }

func (r Rect) Contains(x, y int) bool {
	return r.W > 0 && r.H > 0 && x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H
}

// Layout is the geometry of the last composed frame. Live and MainSkills are
// filled by the content painter, whose row allocation determines their bounds.
type Layout struct {
	Main, Diff, Roster, Live, MainSkills Rect
	Vertical, RosterHorizontal           int
}

// Panes retains local focus and the user's column width, independently of any
// displayed thread or feature model. Pane numbers preserve the saved schema.
type Panes struct {
	Focus, Split int
	DiffOpen     bool
	drag         int
}

func (p *Panes) Select(pane int) {
	switch pane {
	case 0, 3:
		p.Focus = pane
	case 1:
		p.DiffOpen = !p.DiffOpen
		p.Focus = 0
		if p.DiffOpen {
			p.Focus = 1
		}
	}
}

func (p *Panes) Dragging() bool { return p.drag != 0 }
func (p *Panes) StopDrag()      { p.drag = 0 }

// Resize adjusts the column divider; file navigation retains its own width.
// Source: apps/mekugi/internal/router/terminal_ui.go:237:305@70e7ce50 terminalUI.resize
func (p *Panes) Resize(key string, width int) {
	if p.Split == 0 {
		p.Split = max(30, width/2)
	}
	if p.Focus == 3 && (key == "\x1b[A" || key == "k" || key == "\x1b[B" || key == "j") {
		return
	}
	switch key {
	case "\x1b[D", "h":
		p.Split -= 3
	case "\x1b[C", "l":
		p.Split += 3
	}
	p.Split = min(max(30, p.Split), max(30, width-41))
}

// Drag consumes divider gestures and returns the requested navigator width, if
// that divider moved. Roster separators retain their existing pointer capture;
// roster height is determined by its visible rows rather than a stored height.
func (p *Panes) Drag(m Mouse, layout Layout, navigatorWidth int) (handled bool, columns int) {
	if m.Release && p.drag != 0 {
		p.drag = 0
		return true, 0
	}
	if m.Button&32 != 0 && p.drag != 0 {
		switch p.drag {
		case 1:
			p.Split = m.X
		case 3:
			return true, max(16, m.X-layout.Diff.X)
		}
		return true, 0
	}
	if m.Button&^28 == 0 && !m.Release {
		switch {
		case m.X == layout.Vertical && layout.Vertical >= 0 && (layout.RosterHorizontal < 0 || m.Y < layout.RosterHorizontal):
			p.drag = 1
		case m.Y == layout.RosterHorizontal && layout.RosterHorizontal >= 0:
			p.drag = 4
		case layout.Diff.Contains(m.X, m.Y) && navigatorWidth > 0 && m.X-layout.Diff.X == navigatorWidth:
			p.drag = 3
		default:
			return false, 0
		}
		return true, 0
	}
	return false, 0
}

func (p *Panes) RosterLimit(width, height int) int {
	// Reserve Main, the status row and the roster heading before painting.
	// An open wide Diff needs twelve content-frame rows plus roster padding
	// to retain both columns rather than switching to the focused pane.
	mainRows := 8
	if p.DiffOpen && width >= 100 {
		mainRows = 13
	}
	available := max(0, height-mainRows-2)
	if p.Focus == 3 {
		return min(available, max(4, int(float64(height)*0.4)))
	}
	return available
}

// Geometry reserves the status row and roster, then fits Main and saved Diff.
// It returns outer frames, content rectangles and the retained roster row count.
// Source: apps/mekugi/internal/router/native_shell.go:288:334@70e7ce50 terminalUI.paintNative
func (p *Panes) Geometry(width, height, rosterRows int) (layout Layout, left, right Rect, framed bool, keptRoster int) {
	if height-1-rosterRows < 8 {
		rosterRows = 0
	}
	padding := 0
	if rosterRows > 0 && height-1-rosterRows-1 >= 8 {
		padding = 1
	}
	top := max(0, height-1-rosterRows-padding)
	framed = top >= 8
	layout = Layout{Vertical: -1, RosterHorizontal: -1}
	if rosterRows > 0 {
		layout.RosterHorizontal = top
		layout.Roster = Rect{0, top, width, rosterRows}
	}
	switch {
	case p.DiffOpen && width >= 100 && top >= 12:
		split := p.Split
		if split == 0 {
			split = width / 2
		}
		split = min(max(40, split), width-40)
		left, right = Rect{0, 0, split, top}, Rect{split, 0, width - split, top}
		layout.Vertical = split
	case !p.DiffOpen || p.Focus != 1:
		left = Rect{0, 0, width, top}
	default:
		right = Rect{0, 0, width, top}
	}
	content := func(frame Rect) Rect {
		if frame.W < 4 || frame.H < 1 {
			return Rect{}
		}
		if framed {
			return Rect{frame.X + 1, frame.Y + 1, frame.W - 2, frame.H - 2}
		}
		return frame
	}
	layout.Main, layout.Diff = content(left), content(right)
	return layout, left, right, framed, rosterRows
}
