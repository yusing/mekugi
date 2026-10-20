package terminalview

import (
	"slices"
	"strings"
	"testing"
)

func TestCommandOutputRevealAndSettlement(t *testing.T) {
	p := NewCommandOutput(false)
	p.Write("partial")
	frame, changed := p.Advance()
	if !changed || frame.Pending || !slices.Equal(frame.Tail, []string{"partial"}) {
		t.Fatalf("unfinished output: %+v %v", frame, changed)
	}
	if _, changed := p.Advance(); changed {
		t.Fatal("unchanged partial line republished")
	}
	p.Write("\n1\n2\n3\n4\n5\n6\n7\n")
	frame, changed = p.Advance()
	if !changed || !frame.Pending || !slices.Equal(frame.Tail, []string{"partial", "1"}) {
		t.Fatalf("quarter reveal: %+v", frame)
	}
	p.Settle()
	frame, changed = p.Advance()
	if !changed || frame.Pending || frame.Omitted != 3 || !slices.Equal(frame.Tail, []string{"3", "4", "5", "6", "7"}) {
		t.Fatalf("seek settlement: %+v %v", frame, changed)
	}
	if _, changed := p.Advance(); changed {
		t.Fatal("settled frame kept publishing")
	}
	frame.Tail[0] = "mutated"
	p.Invalidate()
	frame, changed = p.Advance()
	if !changed || frame.Tail[0] != "3" {
		t.Fatal("detached frame changed the cursor")
	}
}

func TestCommandOutputInstantAndBoundedBurst(t *testing.T) {
	p := NewCommandOutput(true)
	p.Write(strings.Repeat("line\n", 200))
	if p.Pending() {
		t.Fatal("instant output held completion")
	}
	frame, changed := p.Advance()
	if !changed || frame.Pending || len(frame.Tail) != OutputTailLines || frame.Omitted != 200-OutputTailLines {
		t.Fatalf("instant burst: %+v", frame)
	}
	p = NewCommandOutput(false)
	p.Write(strings.Repeat("line\n", 200))
	frame, changed = p.Advance()
	if !changed || !frame.Pending || len(frame.Tail) != OutputTailLines || frame.Omitted != 200-OutputPendingLines+OutputPendingLines/4-OutputTailLines {
		t.Fatalf("bounded rolling burst: %+v", frame)
	}
}
