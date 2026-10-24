package sessionapi

import (
	"bytes"
	json "encoding/json/v2"
	"errors"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/diffmodel"
	activityfeed "github.com/yusing/mekugi/modules/activity/feed"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	changes "github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

func TestNativeOwnerDeltasThroughQueues(t *testing.T) {
	for _, backlog := range []string{"none", "server", "client"} {
		t.Run(backlog, func(t *testing.T) {
			native := NewLive().Native()
			source, request, failure := native.newView(t.Context(), requestTarget{"thread", "/workspace"}, "", localNativeView)
			if failure != nil {
				t.Fatal(failure)
			}
			defer source.close()
			frame := benchmarkNativeHistoryFrame(1000)
			frame.Agents = &activity.Publication{Entries: []activity.Entry{{Facts: activity.Facts{Text: "agent history"}}}}
			frame.Review = &changes.ReviewView{Revision: 1, Publication: changes.Publication{Files: []changes.File{{Path: "saved.go", Chunks: []changes.Chunk{{Review: diffmodel.ReviewFile{Diff: strings.Repeat("+saved diff\n", 1<<16)}}}}}}}
			frame.Effects = []NativeEffect{{ID: 1, Restore: &composer.Draft{Text: "restore"}}}
			connection := &NativeConnection{thread: "thread", subscription: &RPCSubscription{client: &RPCClient{}}}
			var events []publication
			var frozen []byte
			for step := range 5 {
				frame.View.Version.Revision++
				switch step {
				case 2:
					frame.Main = &activity.Publication{Entries: []activity.Entry{{Facts: activity.Facts{Text: "new history"}}}}
					frame.MainOutput = []activity.OutputResult{{Demand: activity.OutputDemand{Source: 1}, View: activityfeed.OutputView{Lines: []string{"output"}}}}
				case 3:
					frame.MainOutput = nil
				case 4:
					frame.Agents = nil
				}
				if err := native.Publish(request.Attachment, frame); err != nil {
					t.Fatal(err)
				}
				if backlog == "server" {
					continue
				}
				queued, ok := source.native.pop()
				if !ok {
					t.Fatal("lost changed metadata or owner")
				}
				if step == 1 && len(queued.Data) > 16000 {
					t.Fatalf("metadata repeated retained owners: %d bytes", len(queued.Data))
				}
				events = append(events, queued)
				if backlog == "client" {
					continue
				}
				previous := connection.last.Frame
				connection.subscription.events = []RPCEvent{{Kind: queued.Kind, Data: queued.Data}}
				got, err := connection.Next()
				if err != nil {
					t.Fatal(err)
				}
				if step == 1 && (got.Frame.Main != previous.Main || got.Frame.Agents != previous.Agents || got.Frame.Review != previous.Review) {
					t.Fatal("unchanged owner identity was replaced")
				}
				if step == 0 {
					frozen, _ = json.Marshal(got.Frame.Review, nativeEncoding)
				}
				want, _ := json.Marshal(frame.withoutEditors(), nativeEncoding)
				actual, _ := json.Marshal(got.Frame, nativeEncoding)
				if !bytes.Equal(actual, want) {
					t.Fatalf("step %d delta did not reconstruct complete owner state", step)
				}
			}
			if backlog == "server" {
				queued, ok := source.native.pop()
				if !ok {
					t.Fatal("lost snapshot")
				}
				events = []publication{queued}
				if _, ok := source.native.pop(); ok {
					t.Fatal("progress accumulated")
				}
			}
			if backlog != "none" {
				_, subscription := bufferedRPCPublications(t, "view/attach", events)
				connection.subscription = subscription
				got, err := connection.Next()
				if err != nil {
					t.Fatal(err)
				}
				if got.Frame.Main.Entries[0].Text != "new history" || got.Frame.Agents != nil || got.Frame.Review.Revision != 1 || len(got.Frame.Effects) != 1 || len(got.Frame.MainOutput) != 1 {
					t.Fatal("coalescing lost owner state, semantic effect or hydration")
				}
			} else {
				actual, _ := json.Marshal(connection.last.Frame.Review, nativeEncoding)
				if !bytes.Equal(actual, frozen) {
					t.Fatal("delta mutated saved review")
				}
			}
		})
	}
}

func TestNativeSemanticPrefixSurvivesBacklogAndReceipt(t *testing.T) {
	native := NewLive().Native()
	source, attachment, failure := native.newView(t.Context(), requestTarget{"thread", "/workspace"}, "", localNativeView)
	if failure != nil {
		t.Fatal(failure)
	}
	defer source.close()
	frame := nativeTestFrame("thread", 1)
	payload := strings.Repeat("x", 34<<20)
	frame.Effects = []NativeEffect{{ID: 2, Restore: &composer.Draft{Text: payload}}, {ID: 4, Restore: &composer.Draft{Text: payload}}}
	frame.Activity = &NativeActivityFrame{Reset: 90, Effects: []NativeEffect{{ID: 1}, {ID: 3}, {ID: 5}}}
	frame.Notification = &NativeEffect{ID: 91, Notification: &NativeDesktopNotification{Message: "newest"}}
	// An unresolved hydration cannot consume the space needed by the first
	// consequence. It remains reacquirable after receipt advances the prefix.
	frame.MainOutput = []activity.OutputResult{{Demand: activity.OutputDemand{Source: 1}, View: activityfeed.OutputView{Lines: []string{payload}}}}
	for range 2 {
		frame.View.Version.Revision++
		if err := native.Publish(attachment.Attachment, frame); err != nil {
			t.Fatal(err)
		}
	}
	queued, ok := source.native.pop()
	if !ok || queued.Kind != "snapshot" || len(queued.Data) > nativePublicationBytes {
		t.Fatal("paused server lost its bounded initial snapshot")
	}
	if _, ok := source.native.pop(); ok {
		t.Fatal("progress accumulated instead of coalescing")
	}
	_, subscription := bufferedRPCPublications(t, "view/attach", []publication{queued})
	connection := &NativeConnection{subscription: subscription, thread: "thread"}
	got, err := connection.Next()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Frame.Effects) != 1 || got.Frame.Effects[0].ID != 2 || got.Frame.Effects[0].Restore.Text != payload || !got.Frame.PendingEffects || !got.Frame.ReacquireOutput || len(got.Frame.MainOutput) != 0 {
		t.Fatal("first semantic prefix or hydration recovery changed")
	}
	if got.Frame.Activity.Reset != 3 || len(got.Frame.Activity.Effects) != 2 || got.Frame.Notification != nil {
		t.Fatal("activity or notification could acknowledge a withheld consequence")
	}
	if len(frame.Effects) != 2 || frame.PendingEffects || frame.Activity.Reset != 90 || len(frame.Activity.Effects) != 3 || len(frame.MainOutput) != 1 {
		t.Fatal("publication mutated owner-retained payloads")
	}
	// Deliver a real semantic receipt through native admission. The owner alone
	// releases the acknowledged payload; transport keeps no separate effect store.
	body, err := json.Marshal(NativeMutation{Attachment: attachment.Attachment, Version: got.Frame.View.Version, Operation: NativeOperation{Receipt: &NativeReceipt{Effect: 2}}}, nativeEncoding)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan *RPCError, 1)
	go func() {
		_, failure := native.request(t.Context(), requestTarget{"thread", "/workspace"}, "", localNativeView, body, nil)
		done <- failure
	}()
	request := <-native.Requests()
	if request.Operation.Receipt == nil || request.Operation.Receipt.Effect != 2 {
		t.Fatal("receipt did not reach the retention owner")
	}
	frame.Effects = frame.Effects[1:]
	frame.MainOutput = nil
	if err := native.Publish(attachment.Attachment, frame); err != nil {
		t.Fatal(err)
	}
	request.Reply <- ""
	if failure := <-done; failure != nil {
		t.Fatal(failure)
	}
	queued, ok = source.native.pop()
	if !ok || json.Unmarshal(queued.Data, &got, nativeEncoding) != nil || len(got.Frame.Effects) != 1 || got.Frame.Effects[0].ID != 4 || got.Frame.Effects[0].Restore.Text != payload || got.Frame.PendingEffects || got.Frame.Activity.Reset != 90 || got.Frame.Notification == nil {
		t.Fatal("receipt failed to expose the retained suffix and later watermarks")
	}
}

func TestNativeSemanticPrefixCanShrinkBeforeReceipt(t *testing.T) {
	native := NewLive().Native()
	source, attachment, failure := native.newView(t.Context(), requestTarget{"thread", "/workspace"}, "", localNativeView)
	if failure != nil {
		t.Fatal(failure)
	}
	defer source.close()
	frame := nativeTestFrame("thread", 1)
	payload := strings.Repeat("x", 20<<20)
	frame.Effects = []NativeEffect{{ID: 1, Restore: &composer.Draft{Text: payload}}, {ID: 2, Restore: &composer.Draft{Text: payload}}, {ID: 3, Restore: &composer.Draft{Text: payload}}}
	if err := native.Publish(attachment.Attachment, frame); err != nil {
		t.Fatal(err)
	}
	frame.Main = &activity.Publication{Entries: []activity.Entry{{Facts: activity.Facts{Text: strings.Repeat("n", 30<<20)}}}}
	if err := native.Publish(attachment.Attachment, frame); err != nil {
		t.Fatal(err)
	}
	queued, _ := source.native.pop()
	var got NativeEvent
	if err := json.Unmarshal(queued.Data, &got, nativeEncoding); err != nil {
		t.Fatal(err)
	}
	if len(got.Frame.Effects) != 1 || got.Frame.Effects[0].ID != 1 || !got.Frame.PendingEffects {
		t.Fatal("growing state lost the still-unacknowledged suffix")
	}
	frame.Effects = frame.Effects[1:] // receipt of only the folded, visible prefix
	if err := native.Publish(attachment.Attachment, frame); err != nil {
		t.Fatal(err)
	}
	queued, _ = source.native.pop()
	if err := json.Unmarshal(queued.Data, &got, nativeEncoding); err != nil || len(got.Frame.Effects) != 1 || got.Frame.Effects[0].ID != 2 || !got.Frame.PendingEffects {
		t.Fatal("a shorter publication discarded an earlier eligible consequence")
	}
}

func TestNativePublicationRejectsIndivisibleOversize(t *testing.T) {
	payload := strings.Repeat("x", subscriptionBytes)
	for _, kind := range []string{"state", "feed", "consequence"} {
		t.Run(kind, func(t *testing.T) {
			native := NewLive().Native()
			source, attachment, failure := native.newView(t.Context(), requestTarget{"thread", "/workspace"}, "", localNativeView)
			if failure != nil {
				t.Fatal(failure)
			}
			defer source.close()
			frame := nativeTestFrame("thread", 1)
			if kind == "state" {
				frame.Notice = payload
			} else if kind == "feed" {
				frame.Main = &activity.Publication{Entries: []activity.Entry{{Facts: activity.Facts{Text: payload}}}}
			} else {
				frame.Effects = []NativeEffect{{ID: 1, Restore: &composer.Draft{Text: payload}}}
			}
			err := native.Publish(attachment.Attachment, frame)
			if !errors.Is(err, errNativePublicationLimit) || source.ctx.Err() == nil || strings.Contains(err.Error(), "fell behind") {
				t.Fatalf("indivisible resource failure was not explicit: %v", err)
			}
		})
	}
}

func TestNativePublicationBudgetIncludesRPCEnvelope(t *testing.T) {
	params, err := json.Marshal(RPCEvent{Subscription: strings.Repeat("\x00", 256), Kind: "snapshot", Data: []byte(`{}`)}, encoding)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(RPCMessage{JSONRPC: "2.0", Method: "subscription/event", Params: params}, encoding)
	if err != nil || nativePublicationBytes+len(data)-2 != subscriptionBytes {
		t.Fatalf("publication budget does not match admitted RPC framing: %d, %v", len(data), err)
	}
}

func TestNativeOwnerDeltaResetAndReviewReplacement(t *testing.T) {
	native := NewLive().Native()
	source, request, failure := native.newView(t.Context(), requestTarget{"thread", "/workspace"}, "", localNativeView)
	if failure != nil {
		t.Fatal(failure)
	}
	defer source.close()
	frame := benchmarkNativeHistoryFrame(1)
	frame.Review = &changes.ReviewView{Revision: 1, Coverage: "loading", Publication: changes.Publication{Files: []changes.File{{Path: "first.go"}}}}
	connection := &NativeConnection{thread: "thread", subscription: &RPCSubscription{client: &RPCClient{}}}
	var first NativeFrame
	for step := range 6 {
		frame.View.Version.Revision++
		switch step {
		case 1:
			frame.Review = new(*frame.Review)
			frame.Review.Coverage = "complete"
		case 2:
			frame.Review = &changes.ReviewView{Revision: 2, Publication: changes.Publication{Files: []changes.File{{Path: "second.go"}}}}
		case 3:
			frame.View.Version.Attachment++
		case 4:
			frame.View.Main.ID = "replacement"
			frame.Switch = &NativeSwitch{From: "thread", To: "replacement", Origin: request.Attachment}
		case 5:
			frame.Switch = nil
			frame.Review = nil
		}
		if err := native.Publish(request.Attachment, frame); err != nil {
			t.Fatal(err)
		}
		queued, ok := source.native.pop()
		if !ok {
			t.Fatal("lost replacement")
		}
		var wire nativeWireEvent
		if err := json.Unmarshal(queued.Data, &wire, nativeEncoding); err != nil {
			t.Fatal(err)
		}
		if (step == 3 || step == 4) && (queued.Kind != "snapshot" || wire.Retained != 0) {
			t.Fatal("reset retained previous owners")
		}
		connection.subscription.events = []RPCEvent{{Kind: queued.Kind, Data: queued.Data}}
		got, err := connection.Next()
		if err != nil {
			t.Fatal(err)
		}
		if step == 0 {
			first = got.Frame
		}
		want, _ := json.Marshal(frame.withoutEditors(), nativeEncoding)
		actual, _ := json.Marshal(got.Frame, nativeEncoding)
		if !bytes.Equal(actual, want) {
			t.Fatalf("step %d replacement did not reach consumer", step)
		}
	}
	if first.Review.Coverage != "loading" || first.Review.Files[0].Path != "first.go" {
		t.Fatal("replacement mutated prior review")
	}
}

func TestNativeRetainedOwnersKeepSnapshotAndHydrationLimits(t *testing.T) {
	native := NewLive().Native()
	source, request, failure := native.newView(t.Context(), requestTarget{"thread", "/workspace"}, "", localNativeView)
	if failure != nil {
		t.Fatal(failure)
	}
	defer source.close()
	frame := benchmarkNativeHistoryFrame(1)
	frame.Main.Entries[0].Text = strings.Repeat("m", 30<<20)
	frame.Review = &changes.ReviewView{Revision: 1, Publication: changes.Publication{Files: []changes.File{{Chunks: []changes.Chunk{{Review: diffmodel.ReviewFile{Diff: strings.Repeat("r", 30<<20)}}}}}}}
	if err := native.Publish(request.Attachment, frame); err != nil {
		t.Fatal(err)
	}
	snapshot, ok := source.native.pop()
	if !ok || len(snapshot.Data) < 60<<20 || len(snapshot.Data) > nativePublicationBytes {
		t.Fatal("complete near-limit snapshot was lost")
	}
	connection := &NativeConnection{thread: "thread", subscription: &RPCSubscription{client: &RPCClient{}, events: []RPCEvent{{Kind: snapshot.Kind, Data: snapshot.Data}}}}
	if _, err := connection.Next(); err != nil {
		t.Fatal(err)
	}
	payload := strings.Repeat("o", 3<<20)
	frame.MainOutput = []activity.OutputResult{
		{Demand: activity.OutputDemand{Source: 1}, View: activityfeed.OutputView{Lines: []string{payload}}},
		{Demand: activity.OutputDemand{Source: 2}, View: activityfeed.OutputView{Lines: []string{payload}}},
	}
	if err := native.Publish(request.Attachment, frame); err != nil {
		t.Fatal(err)
	}
	delta, ok := source.native.pop()
	if !ok || len(delta.Data) > 4<<20 {
		t.Fatal("hydration did not reserve retained snapshot bytes")
	}
	connection.subscription.events = []RPCEvent{{Kind: delta.Kind, Data: delta.Data}}
	got, err := connection.Next()
	if err != nil || !got.Frame.ReacquireOutput || len(got.Frame.MainOutput) != 1 || got.Frame.MainOutput[0].Demand.Source != 2 {
		t.Fatalf("retained-owner hydration lost recovery identity: %v", err)
	}
	frame.MainOutput = nil
	frame.Effects = []NativeEffect{{ID: 1, Restore: &composer.Draft{Text: strings.Repeat("e", 5<<20)}}}
	if err := native.Publish(request.Attachment, frame); !errors.Is(err, errNativePublicationLimit) {
		t.Fatalf("delta exceeded complete snapshot capacity: %v", err)
	}
}
