package terminalsession

// Rect uses zero-based terminal cells.
type Rect struct{ X, Y, W, H int }

func (r Rect) Contains(x, y int) bool {
	return r.W > 0 && r.H > 0 && x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H
}

// Layout is the geometry of the last composed frame. The content painter
// fills Live once it knows where the dock fits inside Main, and Roster with
// the roster sheet docked at Main's bottom.
type Layout struct {
	Main, Diff, Roster, Live Rect
	Vertical                 int
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
// that divider moved.
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
		case m.X == layout.Vertical && layout.Vertical >= 0 && m.Y < layout.Main.Y+layout.Main.H:
			p.drag = 1
		case layout.Diff.Contains(m.X, m.Y) && navigatorWidth > 0 && m.X-layout.Diff.X == navigatorWidth:
			p.drag = 3
		default:
			return false, 0
		}
		return true, 0
	}
	return false, 0
}

// Geometry reserves the stat rows, then fits Main and saved Diff above them.
// It returns pane bounds and content rectangles.
// Source: apps/mekugi/internal/router/native_shell.go:288:334@70e7ce50 terminalUI.paintNative
func (p *Panes) Geometry(width, height, statRows int) (layout Layout, left, right Rect) {
	top := max(0, height-statRows)
	layout = Layout{Vertical: -1}
	switch {
	case p.DiffOpen && width >= 100 && top >= 12:
		split := p.Split
		if split == 0 {
			split = width / 2
		}
		split = min(max(40, split), width-41)
		left, right = Rect{0, 0, split, top}, Rect{split + 1, 0, width - split - 1, top}
		layout.Vertical = split
	case !p.DiffOpen || p.Focus != 1:
		left = Rect{0, 0, width, top}
	default:
		right = Rect{0, 0, width, top}
	}
	if left.W >= 4 && left.H > 0 {
		layout.Main = left
	}
	if right.W >= 4 && right.H > 0 {
		layout.Diff = right
	}
	return layout, left, right
}
