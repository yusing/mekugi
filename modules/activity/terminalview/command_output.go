package terminalview

// CommandOutput owns a bounded command or segment's terminal reveal cursor.
// Source: apps/mekugi/internal/router/app_server_events.go:52:65,496:501@1bcbdd5a
// and apps/mekugi/internal/router/exec_track.go:34:40,264:269@1bcbdd5a.
type CommandOutput struct {
	tail           OutputTail
	dirty, instant bool
}

type OutputFrame struct {
	Tail    []string
	Omitted int
	Pending bool
}

func NewCommandOutput(instant bool) CommandOutput { return CommandOutput{instant: instant} }

func (p *CommandOutput) Write(delta string) {
	p.tail.Write(delta)
	p.dirty = true
}

// Invalidate makes an admitted source change visible without adding output.
func (p *CommandOutput) Invalidate() { p.dirty = true }

func (p *CommandOutput) Advance() (OutputFrame, bool) {
	changed := p.dirty
	if p.instant {
		changed = p.tail.Flush() || changed
	} else {
		changed = p.tail.Roll() || changed
	}
	p.dirty = false
	return p.frame(), changed
}

// Pending reports animation debt, which never delays instant operations.
func (p *CommandOutput) Pending() bool { return !p.instant && p.tail.Pending() > 0 }

// Settle skips animation but leaves a publication pending for the next frame.
func (p *CommandOutput) Settle() OutputFrame {
	p.tail.Flush()
	p.dirty = true
	return p.frame()
}

func (p *CommandOutput) frame() OutputFrame {
	tail, omitted := p.tail.Lines()
	return OutputFrame{Tail: tail, Omitted: omitted, Pending: p.Pending()}
}
