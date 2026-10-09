package router

import "io"

// drainKeys consumes queued continuation bytes before deciding that a pending
// Escape was a standalone key. Rendering and tick work can exceed the Escape
// timeout even when the terminal delivered a complete sequence promptly.
func (u *appServerUI) drainKeys(keys <-chan byte) error {
	// Bound work so continuous mouse motion or a large paste cannot starve
	// rendering and host events. Exhausting the budget defers Escape expiry.
	for range 256 {
		select {
		case key, ok := <-keys:
			if !ok {
				return io.EOF
			}
			viewed := u.viewedUI()
			viewed.dirty = true
			if err := viewed.shell.key(key); err != nil || viewed.quitRequested {
				return err
			}
		default:
			return u.viewedUI().shell.flushEscape()
		}
	}
	return nil
}
