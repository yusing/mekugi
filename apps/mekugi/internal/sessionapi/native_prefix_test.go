package sessionapi

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"strings"
	"testing"

	activityfeed "github.com/yusing/mekugi/modules/activity/feed"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

func TestNativePublicationReusesEncodedFeeds(t *testing.T) {
	native := NewLive().Native()
	source, request, failure := native.newView(t.Context(), requestTarget{"thread", "/workspace"}, "", localNativeView)
	if failure != nil {
		t.Fatal(failure)
	}
	defer source.close()
	frame := benchmarkNativeHistoryFrame(1000)
	frame.Agents = &activity.Publication{Entries: []activity.Entry{{Facts: activity.Facts{Text: "quoted \"history\" \\ 世界"}}}}
	frame.Effects = []NativeEffect{{ID: 1, Restore: &composer.Draft{Text: "restore"}}}
	feeds := &native.views[request.Attachment].feeds
	var retained []struct{ data, want []byte }
	for step, mutate := range []func(){
		func() {},
		func() {
			frame.View.Version.Revision++
			frame.Facts.Busy = true
		},
		func() {
			frame.MainOutput = []activity.OutputResult{{Demand: activity.OutputDemand{Source: 1}, View: activityfeed.OutputView{Lines: []string{"output"}}}}
		},
		func() { frame.Main, frame.Agents = frame.Agents, frame.Main },
		func() { frame.Agents = nil },
		func() { frame.Main, frame.Agents = frame.Agents, frame.Main },
		func() { frame.Main, frame.Agents = nil, nil },
	} {
		previous := *feeds
		mutate()
		if err := native.Publish(request.Attachment, frame); err != nil {
			t.Fatal(err)
		}
		queued, ok := source.native.pop()
		if !ok {
			t.Fatal("lost changed metadata or feed")
		}
		retained = append(retained, struct{ data, want []byte }{queued.Data, bytes.Clone(queued.Data)})
		// Compare the consuming wire value to ordinary complete-frame encoding,
		// including fields carried by the metadata shadow and hydrated output.
		want, err := json.Marshal(NativeEvent{Attachment: request.Attachment, Frame: frame.withoutEditors()}, nativeEncoding)
		if err != nil {
			t.Fatal(err)
		}
		gotJSON, wantJSON := jsontext.Value(bytes.Clone(queued.Data)), jsontext.Value(want)
		for _, data := range []*jsontext.Value{&gotJSON, &wantJSON} {
			if err := data.Canonicalize(jsontext.CanonicalizeRawInts(false)); err != nil {
				t.Fatal(err)
			}
		}
		if !bytes.Equal(gotJSON, wantJSON) {
			t.Fatal("cached feed publication changed the complete wire value")
		}
		for i, cached := range feeds {
			if cached.source != previous[i].source && cached.data != nil {
				t.Fatal("replacement encoded a feed before reuse")
			}
			if step == 1 && cached.data == nil {
				t.Fatal("reused feed was not cached")
			}
			if cached.source == previous[i].source && previous[i].data != nil && &cached.data[0] != &previous[i].data[0] {
				t.Fatal("metadata or hydration re-encoded an unchanged feed")
			}
			if cached.data == nil {
				continue // replacement frames defer caching until the feed is reused
			}
			if allocs := testing.AllocsPerRun(10, func() { _ = feeds[i].update(cached.source) }); allocs != 0 {
				t.Fatalf("unchanged feed encoding allocated: %v", allocs)
			}
		}
		if err := native.Publish(request.Attachment, frame); err != nil {
			t.Fatal(err)
		}
		if _, ok := source.native.pop(); ok {
			t.Fatal("identical effects frame published again while warming the feed cache")
		}
	}
	for _, previous := range retained {
		if !bytes.Equal(previous.data, previous.want) {
			t.Fatal("later publication mutated retained frame bytes")
		}
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
