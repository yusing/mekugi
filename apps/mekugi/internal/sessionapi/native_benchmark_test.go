package sessionapi

import (
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/diffmodel"
	activityfeed "github.com/yusing/mekugi/modules/activity/feed"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	changes "github.com/yusing/mekugi/modules/changehistory/presentation"
)

func benchmarkNativeHistoryFrame() NativeFrame {
	frame := nativeTestFrame("thread", 1)
	frame.Main = &activity.Publication{Entries: make([]activity.Entry, 1000), LastSeq: 1000}
	text := strings.Repeat("Completed tool output with retained session history. ", 8)
	for i := range frame.Main.Entries {
		frame.Main.Entries[i] = activity.Entry{
			Facts:    activity.Facts{Seq: uint64(i + 1), Kind: "tool", Text: text, OutputComplete: true},
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
	frame := benchmarkNativeHistoryFrame()
	frame.Review = &changes.ReviewView{Revision: 1, Publication: changes.Publication{
		Files: []changes.File{{Path: "large.go", Chunks: []changes.Chunk{{Review: diffmodel.ReviewFile{
			Diff: strings.Repeat("+saved diff\n", 1<<20),
		}}}}},
	}}
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
	for _, changing := range []bool{false, true} {
		name := "UnchangedHistory1000"
		if changing {
			name = "ChangingFrameHistory1000"
		}
		b.Run(name, func(b *testing.B) {
			n := NewLive().Native()
			source, request, failure := n.newView(b.Context(), requestTarget{"thread", "/workspace"}, "", authority)
			if failure != nil {
				b.Fatal(failure)
			}
			defer source.close()
			frame := benchmarkNativeHistoryFrame()
			if err := n.Publish(request.Attachment, frame); err != nil {
				b.Fatal(err)
			}
			if _, ok := source.native.pop(); !ok {
				b.Fatal("missing initial publication")
			}
			b.ReportAllocs()
			for b.Loop() {
				if changing {
					frame.View.Version.Revision++
				}
				if err := n.Publish(request.Attachment, frame); err != nil {
					b.Fatal(err)
				}
				if changing {
					if _, ok := source.native.pop(); !ok {
						b.Fatal("missing changed publication")
					}
				}
			}
			if _, ok := source.native.pop(); ok {
				b.Fatal("unexpected queued publication")
			}
		})
	}
}

func BenchmarkNormalizeNativeOutput(b *testing.B) {
	for _, hydration := range []bool{false, true} {
		name := "StateHistory1000"
		if hydration {
			name = "HydrationHistory1000"
		}
		b.Run(name, func(b *testing.B) {
			frame := benchmarkNativeHistoryFrame()
			if hydration {
				frame.MainOutput = []activity.OutputResult{{
					Demand: activity.OutputDemand{Source: 1, Output: 1, Version: 1},
					View:   activityfeed.OutputView{Lines: []string{strings.Repeat("x", 1<<20)}},
				}}
			}
			data, err := encodeNativePrefix("benchmark", "", frame)
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
