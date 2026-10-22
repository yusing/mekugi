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
	frame.Notice = strings.Repeat("n", 30<<20)
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
	for _, kind := range []string{"state", "consequence"} {
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
