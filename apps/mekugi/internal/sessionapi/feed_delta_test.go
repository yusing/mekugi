package sessionapi

import (
	"bytes"
	json "encoding/json/v2"
	"fmt"
	"slices"
	"strings"
	"testing"

	activity "github.com/yusing/mekugi/modules/activity/presentation"
)

func TestFeedDeltaPreservesCompleteWindows(t *testing.T) {
	s := NewLive()
	viewer, failure := s.subscribe(t.Context())
	if failure != nil {
		t.Fatal(failure)
	}
	defer viewer.close()
	f := Frame{Session: Session{Thread: "thread"}, Main: benchmarkNativeHistoryFrame(80).Main}
	for i := range f.Main.Entries {
		f.Main.Entries[i].Agent, f.Main.Entries[i].Kind, f.Main.Entries[i].Text = "Main", "text", strings.Repeat("x", 400)
	}
	var merged publication
	publish := func() (*feedPatch, *feedPatch) {
		t.Helper()
		if err := s.Publish(f); err != nil {
			t.Fatal(err)
		}
		event := <-viewer.events
		var patch message
		if err := json.Unmarshal(event.Data, &patch); err != nil {
			t.Fatal(err)
		}
		if event.Kind == "snapshot" {
			merged = event
		} else {
			var ok bool
			merged, ok = coalesceShared(merged, event)
			if !ok {
				t.Fatal("cannot apply feed patch")
			}
		}
		var restored, snapshot message
		full, err := s.snapshot()
		if err != nil || json.Unmarshal(full.Data, &snapshot) != nil || json.Unmarshal(merged.Data, &restored) != nil {
			t.Fatal("cannot decode full snapshot", err)
		}
		if !slices.Equal(restored.Main.Order, snapshot.Main.Order) || len(restored.Main.Entries) != len(snapshot.Main.Entries) || restored.Main.Revision != snapshot.Main.Revision {
			t.Fatal("deltas did not reconstruct current window")
		}
		for i := range snapshot.Main.Entries {
			if !bytes.Equal(restored.Main.Entries[i], snapshot.Main.Entries[i]) {
				t.Fatal("delta reconstruction differs from snapshot")
			}
		}
		return patch.Main, snapshot.Main
	}
	publish()
	for step := range 4 {
		f.Main = new(*f.Main)
		f.Main.Entries = slices.Clone(f.Main.Entries)
		index := len(f.Main.Entries) - 1
		if step == 3 {
			index = 0 // Earlier mutation changes history identity, requiring a full window.
		}
		f.Main.Entries[index].Revision++
		f.Main.Entries[index].Text = []string{strings.Repeat("y", 400), strings.Repeat("large", 300), "small", "historical"}[step]
		patch, full := publish()
		if step == 0 && len(patch.Entries) != 1 || step == 2 && len(patch.Entries) <= 1 || step == 3 && len(patch.Entries) != len(full.Entries) {
			t.Fatalf("step %d sent %d of %d entries", step, len(patch.Entries), len(full.Entries))
		}
	}
}

func BenchmarkSharedFeedTail(b *testing.B) {
	for _, count := range []int{50, 500, 2000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			s := NewLive()
			f := Frame{Session: Session{Thread: "thread"}, Main: benchmarkNativeHistoryFrame(count).Main}
			for i := range f.Main.Entries {
				f.Main.Entries[i].Agent, f.Main.Entries[i].Kind = "Main", "text"
			}
			if err := s.Publish(f); err != nil {
				b.Fatal(err)
			}
			sub, failure := s.subscribe(b.Context())
			if failure != nil {
				b.Fatal(failure)
			}
			defer sub.close()
			<-sub.events
			b.ReportAllocs()
			var size int
			for b.Loop() {
				f.Main = &activity.Publication{Entries: slices.Clone(f.Main.Entries)}
				e := &f.Main.Entries[count-1]
				e.Revision++
				e.Text = fmt.Sprint(e.Revision)
				if err := s.Publish(f); err != nil {
					b.Fatal(err)
				}
				size = len((<-sub.events).Data)
			}
			b.ReportMetric(float64(size), "wire-B/frame")
		})
	}
}
