package sessionapi

import (
	json "encoding/json/v2"
	"strings"
	"testing"

	activityfeed "github.com/yusing/mekugi/modules/activity/feed"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

func TestNativeDistinctOutputBacklogRemainsReacquirable(t *testing.T) {
	for _, queue := range []string{"server", "client"} {
		t.Run(queue, func(t *testing.T) {
			native := NewLive().Native()
			source, attachment, failure := native.newView(t.Context(), requestTarget{"thread", "/workspace"}, "", localNativeView)
			if failure != nil {
				t.Fatal(failure)
			}
			defer source.close()
			frame := nativeTestFrame("thread", 1)
			frame.Effects = []NativeEffect{{ID: 1, Restore: &composer.Draft{Text: "retained draft"}}}
			payload := strings.Repeat("x", 2<<20)
			var events []publication
			for i := range 7 {
				frame.View.Version.Revision = uint64(i + 1)
				frame.MainOutput = []activity.OutputResult{{Demand: activity.OutputDemand{Source: uint64(i + 1), Output: 1, Version: 1}, View: activityfeed.OutputView{Lines: []string{payload}}}}
				if i == 6 {
					// A later ordinary frame must preserve the earlier compaction
					// marker even though its producer has no hydration to send.
					frame.MainOutput = nil
				}
				if queue == "server" {
					if err := native.Publish(attachment.Attachment, frame); err != nil {
						t.Fatalf("transient output %d retired the interaction: %v", i, err)
					}
				} else {
					events = append(events, encodeHydrationFrame(t, attachment.Attachment, frame, i == 0))
				}
			}
			if queue == "server" {
				event, ok := source.native.pop()
				if !ok {
					t.Fatal("server lost the current publication")
				}
				events = []publication{event}
				if _, ok := source.native.pop(); ok {
					t.Fatal("reacquirable output retained superseded state frames")
				}
			}
			_, subscription := bufferedRPCPublications(t, "view/attach", events)
			connection := &NativeConnection{subscription: subscription, thread: "thread"}
			got, err := connection.Next()
			if err != nil || !got.Frame.ReacquireOutput || got.Attachment != attachment.Attachment || got.Frame.View.Version.Revision != 7 {
				t.Fatalf("missing catch-up state or reacquisition marker: revision=%d marker=%v err=%v", got.Frame.View.Version.Revision, got.Frame.ReacquireOutput, err)
			}
			if len(got.Frame.Effects) != 1 || got.Frame.Effects[0].Restore.Text != "retained draft" || len(got.Frame.MainOutput) != 3 {
				t.Fatalf("transient compaction changed semantic effects or retained window: effects=%d outputs=%d", len(got.Frame.Effects), len(got.Frame.MainOutput))
			}
			for i, output := range got.Frame.MainOutput {
				if output.Demand.Source != uint64(4+i) || output.View.Lines[0] != payload {
					t.Fatalf("newest whole output window changed at %d", i)
				}
			}
		})
	}
}

func TestNativeFirstHydrationPublicationUsesBoundedWindow(t *testing.T) {
	for _, count := range []int{1, 2} {
		t.Run(map[int]string{1: "large-single", 2: "first-window"}[count], func(t *testing.T) {
			native := NewLive().Native()
			source, attachment, failure := native.newView(t.Context(), requestTarget{"thread", "/workspace"}, "", localNativeView)
			if failure != nil {
				t.Fatal(failure)
			}
			defer source.close()
			frame := nativeTestFrame("thread", 1)
			payload := strings.Repeat("x", nativeOutputBacklogBytes+1)
			for i := range count {
				frame.MainOutput = append(frame.MainOutput, activity.OutputResult{Demand: activity.OutputDemand{Source: uint64(i + 1)}, View: activityfeed.OutputView{Lines: []string{payload}}})
			}
			if err := native.Publish(attachment.Attachment, frame); err != nil {
				t.Fatal(err)
			}
			event, ok := source.native.pop()
			var serverEvent NativeEvent
			if !ok || json.Unmarshal(event.Data, &serverEvent, nativeEncoding) != nil || len(serverEvent.Frame.MainOutput) != 1 || serverEvent.Frame.ReacquireOutput != (count > 1) {
				t.Fatal("server did not normalize its first hydration publication")
			}
			// Feed the original unnormalized first frame through the RPC reader.
			_, subscription := bufferedRPCPublications(t, "view/attach", []publication{encodeHydrationFrame(t, attachment.Attachment, frame, true)})
			connection := &NativeConnection{subscription: subscription, thread: "thread"}
			got, err := connection.Next()
			if err != nil || got.Frame.ReacquireOutput != (count > 1) || len(got.Frame.MainOutput) != 1 || got.Frame.MainOutput[0].Demand.Source != uint64(count) || got.Frame.MainOutput[0].View.Lines[0] != payload {
				t.Fatalf("first frame changed a complete result or omitted its compaction marker: outputs=%d marker=%v err=%v", len(got.Frame.MainOutput), got.Frame.ReacquireOutput, err)
			}
		})
	}
}

func encodeHydrationFrame(t *testing.T, attachment string, frame NativeFrame, first bool) publication {
	t.Helper()
	data, err := json.Marshal(NativeEvent{Attachment: attachment, Frame: frame}, nativeEncoding)
	if err != nil {
		t.Fatal(err)
	}
	kind := "patch"
	if first {
		kind = "snapshot"
	}
	return publication{Kind: kind, Data: data}
}
