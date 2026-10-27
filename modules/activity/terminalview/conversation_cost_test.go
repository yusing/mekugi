package terminalview

import (
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/yusing/mekugi/modules/activity/presentation"
)

func settledConversation(count int) *View {
	v := NewView()
	v.Conversation = true
	now := time.Unix(100, 0)
	v.Clock = func() time.Time { return now }
	p := &presentation.Publication{}
	for i := range count {
		agent, kind := "Main", "text"
		if i%2 == 0 {
			agent, kind = "You", "user"
		}
		p.Entries = append(p.Entries, presentation.Entry{Facts: presentation.Facts{Seq: uint64(i + 1), Agent: agent, Kind: kind, Text: "A short unchanged retained message.", Observed: now}, Revision: 1})
	}
	v.Update(p, Display{Caller: "/root"})
	return v
}

func TestConversationScratchKeepsFeedsAndFrozenViewsIndependent(t *testing.T) {
	v := settledConversation(80)
	feed := v.RenderFeed(100, 30)
	lines, spans := slices.Clone(feed.Lines), slices.Clone(feed.Spans)
	frozen := v.FreezeTranscript()
	want := slices.Clone(frozen.RenderFeed(100, 30).Lines)
	for _, width := range []int{60, 100, 120, 100} {
		v.Expansion = (v.Expansion + 1) % 3
		v.RenderFeed(width, 30)
		if got := frozen.RenderFeed(100, 30).Lines; !slices.Equal(got, want) {
			t.Fatal("live scratch changed frozen renderer")
		}
	}
	if !slices.Equal(feed.Lines, lines) || !reflect.DeepEqual(feed.Spans, spans) {
		t.Fatal("later rendering mutated an earlier feed")
	}
}

func BenchmarkWarmTranscript(b *testing.B) {
	for _, count := range []int{50, 500, 2000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			v := settledConversation(count)
			v.RenderFeed(100, 30)
			b.ReportAllocs()
			for b.Loop() {
				v.RenderFeed(100, 30)
			}
		})
	}
}
