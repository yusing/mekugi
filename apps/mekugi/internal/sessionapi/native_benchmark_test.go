package sessionapi

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/diffmodel"
	activityfeed "github.com/yusing/mekugi/modules/activity/feed"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	changes "github.com/yusing/mekugi/modules/changehistory/presentation"
)

func benchmarkNativeHistoryFrame(entries int) NativeFrame {
	frame := nativeTestFrame("thread", 1)
	frame.Main = &activity.Publication{Entries: make([]activity.Entry, entries), LastSeq: uint64(entries)}
	text := strings.Repeat("Completed tool output with retained session history. ", 8)
	for i := range frame.Main.Entries {
		frame.Main.Entries[i] = activity.Entry{
			Seq: uint64(i + 1), Kind: "tool", Text: text, OutputComplete: true,
			Revision: 1,
		}
	}
	return frame
}

func BenchmarkNativePublishSavedReview(b *testing.B) {
	n := NewLive().Native()
	source, request, failure := n.newView(b.Context(), requestTarget{"thread", "/workspace"}, "", localNativeView)
	if failure != nil {
		b.Fatal(failure)
	}
	defer source.close()
	frame := benchmarkNativeHistoryFrame(1000)
	frame.Review = &changes.ReviewView{Revision: 1,
		Files: []changes.File{{Path: "large.go", Chunks: []changes.Chunk{{Review: diffmodel.ReviewFile{
			Diff: strings.Repeat("+saved diff\n", 1<<20),
		}}}}}}
	if err := n.Publish(request.Attachment, frame); err != nil {
		b.Fatal(err)
	}
	source.native.pop()
	b.ReportAllocs()
	for b.Loop() {
		frame.Review = new(*frame.Review)
		if err := n.Publish(request.Attachment, frame); err != nil {
			b.Fatal(err)
		}
	}
	if _, ok := source.native.pop(); ok {
		b.Fatal("unchanged review published again")
	}
}

func BenchmarkNativePublish(b *testing.B) {
	for _, authority := range []viewAuthority{localNativeView, remoteHumanView} {
		name := "Local"
		if authority == remoteHumanView {
			name = "RemoteHuman"
		}
		b.Run(name, func(b *testing.B) {
			benchmarkNativePublish(b, authority)
		})
	}
}

func benchmarkNativePublish(b *testing.B, authority viewAuthority) {
	for _, entries := range []int{1, 1000, 10000} {
		for _, mode := range []string{"Unchanged", "ChangingFrame", "ReplacingFeed", "QueuedFrame"} {
			name := fmt.Sprintf("%sHistory%d", mode, entries)
			b.Run(name, func(b *testing.B) {
				n := NewLive().Native()
				source, request, failure := n.newView(b.Context(), requestTarget{"thread", "/workspace"}, "", authority)
				if failure != nil {
					b.Fatal(failure)
				}
				defer source.close()
				frame := benchmarkNativeHistoryFrame(entries)
				if err := n.Publish(request.Attachment, frame); err != nil {
					b.Fatal(err)
				}
				if _, ok := source.native.pop(); !ok {
					b.Fatal("missing initial publication")
				}
				b.ReportAllocs()
				for b.Loop() {
					if mode != "Unchanged" {
						frame.View.Version.Revision++
					}
					if mode == "ReplacingFeed" {
						frame.Main = new(*frame.Main)
						frame.Main.LastSeq++
					}
					if err := n.Publish(request.Attachment, frame); err != nil {
						b.Fatal(err)
					}
					if mode == "ChangingFrame" || mode == "ReplacingFeed" {
						if _, ok := source.native.pop(); !ok {
							b.Fatal("missing changed publication")
						}
					}
				}
				if _, ok := source.native.pop(); ok != (mode == "QueuedFrame") {
					b.Fatal("unexpected queued publication")
				}
				if _, ok := source.native.pop(); ok {
					b.Fatal("progress accumulated instead of coalescing")
				}
			})
		}
	}
}

func BenchmarkNormalizeNativeOutput(b *testing.B) {
	for _, hydration := range []bool{false, true} {
		name := "StateHistory1000"
		if hydration {
			name = "HydrationHistory1000"
		}
		b.Run(name, func(b *testing.B) {
			frame := benchmarkNativeHistoryFrame(1000)
			if hydration {
				frame.MainOutput = []activity.OutputResult{{
					Demand: activity.OutputDemand{Source: 1, Output: 1, Version: 1},
					View:   activityfeed.OutputView{Lines: []string{strings.Repeat("x", 1<<20)}},
				}}
			}
			data, err := encodeNativePrefix("benchmark", "", frame, 0, new([4]int), nil)
			if err != nil {
				b.Fatal(err)
			}
			if len(data) == 0 || len(data) >= nativeOutputBacklogBytes {
				b.Fatalf("expected nonempty below-budget publication, got %d bytes", len(data))
			}
			event := publication{Kind: "patch", Data: data}
			var normalized publication
			b.ReportAllocs()
			for b.Loop() {
				normalized = normalizeNativeOutput(event)
			}
			if normalized.Kind != event.Kind || len(normalized.Data) != len(data) || &normalized.Data[0] != &data[0] {
				b.Fatal("normalization replaced a below-budget publication")
			}
		})
	}
}

// Representative owner sizes: about 52 MB saved review, 6 MB main history and
// 2 MB agent history. Includes publication, server queue and typed consumption.
func BenchmarkNativeWireLargeOwners(b *testing.B) {
	for _, changingEntry := range []bool{false, true} {
		name := "Metadata"
		if changingEntry {
			name = "OneEntry"
		}
		b.Run(name, func(b *testing.B) {
			n := NewLive().Native()
			source, request, failure := n.newView(b.Context(), requestTarget{"thread", "/workspace"}, "", localNativeView)
			if failure != nil {
				b.Fatal(failure)
			}
			defer source.close()
			frame := benchmarkNativeHistoryFrame(476)
			for i := range frame.Main.Entries {
				frame.Main.Entries[i].Text = strings.Repeat("history ", 1500)
			}
			frame.Agents = &activity.Publication{Entries: slices.Clone(frame.Main.Entries[:33])}
			for i := range frame.Agents.Entries {
				frame.Agents.Entries[i].Text = strings.Repeat("agent ", 10000)
			}
			file := changes.File{Path: "saved.go", Chunks: []changes.Chunk{{Review: diffmodel.ReviewFile{Diff: strings.Repeat("+saved diff\n", 2<<20)}}}}
			frame.Review = &changes.ReviewView{Revision: 1, Files: []changes.File{file}, Unfiltered: map[string]changes.File{"saved.go": file}}
			connection := &NativeConnection{thread: "thread", subscription: &RPCSubscription{client: &RPCClient{}}}
			consume := func() int {
				if err := n.Publish(request.Attachment, frame); err != nil {
					b.Fatal(err)
				}
				queued, ok := source.native.pop()
				if !ok {
					b.Fatal("missing publication")
				}
				connection.subscription.events = []RPCEvent{{Kind: queued.Kind, Data: queued.Data}}
				if _, err := connection.Next(); err != nil {
					b.Fatal(err)
				}
				return len(queued.Data)
			}
			snapshotBytes := consume()
			b.ReportAllocs()
			bytes := 0
			for b.Loop() {
				frame.View.Version.Revision++
				if changingEntry {
					frame.Main = new(*frame.Main)
					frame.Main.Entries = slices.Clone(frame.Main.Entries)
					frame.Main.Entries[0].Revision++
					frame.Main.Entries[0].Text = fmt.Sprint(frame.View.Version.Revision)
				}
				bytes = consume()
			}
			b.ReportMetric(float64(snapshotBytes), "snapshot-bytes")
			b.ReportMetric(float64(bytes), "update-bytes")
		})
	}
}
