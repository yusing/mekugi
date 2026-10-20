package activity

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestActivityTimingVisibleForClassifiedRead(t *testing.T) {
	block := Block{Kind: "reads", Verb: "Read", Reads: []Read{{Path: "sample.txt"}}, Code: "cat sample.txt", Duration: 125 * time.Millisecond}
	var p Painter
	page := p.DialogPage(block, 80)
	title := ansi.Strip(page.Title)
	if !strings.Contains(title, "125ms") || page.Text != block.Code {
		t.Fatalf("title=%q copy=%q", title, page.Text)
	}
	feed := ansi.Strip(strings.Join(p.Block(block, 80), "\n"))
	if !strings.Contains(feed, "125ms") {
		t.Fatalf("feed elapsed missing: %q", feed)
	}
}
