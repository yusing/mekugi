package router

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/yusing/mekugi/libraries/diffrender"
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

func TestLiveDiffEscapeTimeoutPreservesQueuedReply(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newLiveDiffTerminalController(nil, "", nil)
		defer c.close()
		c.theme = diffrender.DarkTheme
		c.navigation.Filtering = true
		c.handleKey(27)
		keys := make(chan byte, 32)
		for _, key := range []byte("]11;rgb:ffff/ffff/ffff\x1b\\") {
			keys <- key
		}
		time.Sleep(time.Second)
		<-c.escapeC
		if c.expireEscape(keys) {
			t.Fatal("terminal reply quit the harness")
		}
		if c.escapeC != nil {
			t.Fatal("OSC opener left the lone-Escape timer armed")
		}
		for len(keys) > 0 {
			c.handleKey(<-keys)
		}
		if c.theme != diffrender.LightTheme || !c.navigation.Filtering {
			t.Fatalf("queued reply changed navigation or lost theme: theme=%v filtering=%t", c.theme, c.navigation.Filtering)
		}
		c.handleKey(27)
		time.Sleep(time.Second)
		<-c.escapeC
		if c.expireEscape(keys) || c.escape != "" || c.navigation.Filtering {
			t.Fatal("lone Escape did not dismiss navigation")
		}
	})
}
