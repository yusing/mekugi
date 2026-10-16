package router

import (
	"testing"
)

func TestLiveDiffOSCDisarmsEscapeTimer(t *testing.T) {
	c := newLiveDiffTerminalController(nil, "", nil)
	defer c.close()
	c.handleKey(27)
	c.handleKey(']')
	if c.escapeC != nil {
		t.Fatal("OSC opener left the lone-Escape timer armed")
	}
}
