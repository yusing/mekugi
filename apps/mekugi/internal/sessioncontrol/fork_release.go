package sessioncontrol

import "sync/atomic"

const (
	forkLive uint32 = iota
	forkInterrupting
	forkClosing
	forkClosed
)

// forkRelease separates acknowledged cleanup from an unsolicited host close.
// The event loop writes it; inference workers may read Unloaded concurrently.
type forkRelease struct {
	phase      atomic.Uint32
	hostClosed atomic.Bool
}

func (f *forkRelease) Next(turn, closeMethod string) string {
	if f.phase.Load() != forkLive {
		return ""
	}
	if turn != "" && !f.hostClosed.Load() {
		f.phase.Store(forkInterrupting)
		return "turn/interrupt"
	}
	f.phase.Store(forkClosing)
	return closeMethod
}

func (f *forkRelease) InterruptAcknowledged() {
	f.phase.CompareAndSwap(forkInterrupting, forkLive)
}

func (f *forkRelease) Failed() {
	if f.phase.Load() != forkClosed {
		f.phase.Store(forkLive)
	}
}

func (f *forkRelease) HostClosed()    { f.hostClosed.Store(true) }
func (f *forkRelease) Complete()      { f.phase.Store(forkClosed) }
func (f *forkRelease) Closed() bool   { return f.phase.Load() == forkClosed }
func (f *forkRelease) Unloaded() bool { return f.hostClosed.Load() || f.Closed() }
