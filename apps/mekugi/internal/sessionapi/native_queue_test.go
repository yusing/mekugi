package sessionapi

import (
	json "encoding/json/v2"
	"errors"
	"strings"
	"testing"

	activityfeed "github.com/yusing/mekugi/modules/activity/feed"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

func TestNativeJoinedExitHasReservedServerAndClientCapacity(t *testing.T) {
	native := NewLive().Native()
	source, attachment, failure := native.newView(t.Context(), requestTarget{"thread", "/workspace"}, "", localNativeView)
	if failure != nil {
		t.Fatal(failure)
	}
	defer source.close()
	for i := range subscriberBuffer {
		// Acquired output belongs to distinct presentation attachments, so
		// these deliveries cannot be moved into a single latest frame.
		frame := nativeTestFrame("thread", uint64(i+1))
		frame.MainOutput = []activity.OutputResult{{Demand: activity.OutputDemand{Source: uint64(i + 1)}}}
		if err := native.Publish(attachment.Attachment, frame); err != nil {
			t.Fatal(err)
		}
	}
	native.Finish(&NativeExit{Code: 17, Message: "exact joined result"})
	var events []publication
	for {
		event, ok := source.native.pop()
		if !ok {
			break
		}
		events = append(events, event)
	}
	if len(events) != subscriberBuffer+1 {
		t.Fatalf("server dropped an ordered delivery or exit at capacity: %d", len(events))
	}
	_, subscription := bufferedRPCPublications(t, "view/attach", events)
	connection := &NativeConnection{subscription: subscription, thread: "thread"}
	for i := range subscriberBuffer {
		event, err := connection.Next()
		if err != nil || len(event.Frame.MainOutput) != 1 || event.Frame.MainOutput[0].Demand.Source != uint64(i+1) {
			t.Fatalf("delivery %d lost before exit: %+v, %v", i, event.Frame.MainOutput, err)
		}
	}
	if _, err := connection.Next(); err == nil {
		t.Fatal("joined exit was not delivered")
	} else if exit, ok := errors.AsType[*NativeExit](err); !ok || exit.Code != 17 || exit.Message != "exact joined result" {
		t.Fatalf("joined exit changed: %v", err)
	}
}

func TestNativeRepeatedOutputBacklogPreservesDemandIdentity(t *testing.T) {
	const sourceID = uint64(18446744073709551614)
	demands := []activity.OutputDemand{
		{Source: sourceID, Output: 7, Version: 3},
		{Source: sourceID, Output: 7, Version: 3, Bytes: 128},
		{Source: sourceID, Output: 7, Version: 2},
		{Source: sourceID, Output: 8, Version: 3},
		{Source: 42, Output: 7, Version: 3},
	}
	large := strings.Repeat("x", 2<<20)
	for _, queue := range []string{"server", "client"} {
		t.Run(queue, func(t *testing.T) {
			native := NewLive().Native()
			source, attachment, failure := native.newView(t.Context(), requestTarget{"thread", "/workspace"}, "", localNativeView)
			if failure != nil {
				t.Fatal(failure)
			}
			defer source.close()
			frame := nativeTestFrame("thread", 1)
			for _, demand := range demands {
				frame.MainOutput = append(frame.MainOutput, activity.OutputResult{Demand: demand, View: activityfeed.OutputView{Lines: []string{"independent result"}}})
			}
			frame.MainOutput[0].View.Lines = []string{large}
			frame.AgentOutput = []activity.OutputResult{{Demand: demands[0], View: activityfeed.OutputView{Lines: []string{"independent feed"}}}}
			encode := func(kind string) publication {
				t.Helper()
				data, err := json.Marshal(NativeEvent{Attachment: attachment.Attachment, Frame: frame}, nativeEncoding)
				if err != nil {
					t.Fatal(err)
				}
				return publication{Kind: kind, Data: data}
			}
			events := []publication{encode("snapshot")}
			if queue == "server" {
				if err := native.Publish(attachment.Attachment, frame); err != nil {
					t.Fatal(err)
				}
			}
			frame.MainOutput = frame.MainOutput[:1]
			// Newest equivalent acquisition wins without changing independent
			// byte budgets, versions, output IDs, sources or the other feed.
			frame.MainOutput[0].Limited = true
			repeated := encode("patch")
			for i := range 33 {
				if queue == "server" {
					frame.View.Version.Revision = uint64(i + 2)
					if err := native.Publish(attachment.Attachment, frame); err != nil {
						t.Fatal("repeated hydration retired the view:", err)
					}
				} else {
					events = append(events, repeated)
				}
			}
			if queue == "server" {
				event, ok := source.native.pop()
				if !ok {
					t.Fatal("server lost the hydration snapshot")
				}
				events = []publication{event}
				if _, ok := source.native.pop(); ok {
					t.Fatal("repeated output accumulated server deliveries")
				}
			}
			client, subscription := bufferedRPCPublications(t, "view/attach", events)
			client.mu.Lock()
			queuedBytes, subscriptionError := subscription.bytes, subscription.err
			client.mu.Unlock()
			if queuedBytes > 3<<20 || subscriptionError != nil {
				t.Fatalf("repeated hydration accumulated bytes: %d, %v", queuedBytes, subscriptionError)
			}
			connection := &NativeConnection{subscription: subscription, thread: "thread"}
			got, err := connection.Next()
			if err != nil || len(got.Frame.MainOutput) != len(demands) || len(got.Frame.AgentOutput) != 1 {
				t.Fatalf("hydration identity lost: main=%d agents=%d, %v", len(got.Frame.MainOutput), len(got.Frame.AgentOutput), err)
			}
			results := make(map[activity.OutputDemand]activity.OutputResult)
			for _, output := range got.Frame.MainOutput {
				results[output.Demand] = output
			}
			for _, demand := range demands {
				if _, exists := results[demand]; !exists {
					t.Fatalf("demand was lost: %+v", demand)
				}
			}
			if !results[demands[0]].Limited || results[demands[0]].View.Lines[0] != large || got.Frame.AgentOutput[0].View.Lines[0] != "independent feed" {
				t.Fatal("latest acquisition or feed separation was lost")
			}
		})
	}
}

func TestNativeSnapshotBacklogKeepsAttachmentAndUnacknowledgedEffects(t *testing.T) {
	native := NewLive().Native()
	source, attachment, failure := native.newView(t.Context(), requestTarget{"thread", "/workspace"}, "", localNativeView)
	if failure != nil {
		t.Fatal(failure)
	}
	defer source.close()
	for i := range 200 {
		frame := nativeTestFrame("thread", uint64(i+1))
		frame.Effects = []NativeEffect{{ID: 1, Restore: &composer.Draft{Text: "unsent input"}}}
		if err := native.Publish(attachment.Attachment, frame); err != nil {
			t.Fatal(err)
		}
	}
	event, ok := source.native.pop()
	if !ok || event.Kind != "snapshot" {
		t.Fatal("initial snapshot marker was lost")
	}
	if _, ok := source.native.pop(); ok {
		t.Fatal("complete snapshots accumulated")
	}
	_, subscription := bufferedRPCPublications(t, "view/attach", []publication{event})
	connection := &NativeConnection{subscription: subscription, thread: "thread"}
	got, err := connection.Next()
	if err != nil || got.Attachment != attachment.Attachment || got.Frame.View.Version.Attachment != 200 || len(got.Frame.Effects) != 1 || got.Frame.Effects[0].Restore.Text != "unsent input" {
		t.Fatalf("snapshot catch-up changed the interaction: %+v, %v", got, err)
	}
}
