package sessionapi

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	activity "github.com/yusing/mekugi/modules/activity/presentation"
)

func TestRPCSharedBacklogPreservesFeedAndReplacementSemantics(t *testing.T) {
	events := []publication{
		{Kind: "snapshot", Data: jsontext.Value(`{"session":{"thread":"thread","title":"old"},"status":{"failure":true},"main":{"entries":[{"seq":18446744073709551614,"text":"old","running":true},{"seq":2,"text":"removed"}],"order":[18446744073709551614,2]},"agents":{"entries":[{"seq":18446744073709551614,"text":"other feed"}],"order":[18446744073709551614]}}`)},
		{Kind: "patch", Data: jsontext.Value(`{"session":{"thread":"thread"},"main":{"entries":[{"seq":18446744073709551614,"text":"replacement"}]}}`)},
		{Kind: "patch", Data: jsontext.Value(`{"main":{"order":[]}}`)},
		{Kind: "patch", Data: jsontext.Value(`{"main":{"entries":[{"seq":18446744073709551614,"text":"reinserted","exact":18446744073709551615},{"seq":3,"text":"third","running":true}],"order":[18446744073709551614,3]}}`)},
		{Kind: "patch", Data: jsontext.Value(`{"main":{"entries":[{"seq":3,"text":"complete replacement"}]},"agents":{"order":[]}}`)},
	}
	for i := range 200 {
		events = append(events, publication{Kind: "patch", Data: jsontext.Value(fmt.Sprintf(`{"status":{"text":"progress-%d"}}`, i))})
	}
	client, subscription := bufferedRPCPublications(t, "session/subscribe", events)
	client.mu.Lock()
	queued := len(subscription.events)
	client.mu.Unlock()
	if queued != 1 {
		t.Fatalf("reconstructible progress retained %d queued frames", queued)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := subscription.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled consumer ignored cancellation:", err)
	}
	event, err := subscription.Next(t.Context())
	var got message
	if err != nil || event.Kind != "snapshot" || json.Unmarshal(event.Data, &got) != nil {
		t.Fatalf("catch-up snapshot unavailable: %s %v", event.Data, err)
	}
	if got.Session.Title != "" || string(got.Status) != `{"text":"progress-199"}` || !reflect.DeepEqual(got.Main.Order, []uint64{18446744073709551614, 3}) || len(got.Main.Entries) != 2 || got.Agents.Order == nil || len(got.Agents.Order) != 0 || len(got.Agents.Entries) != 0 {
		t.Fatalf("catch-up changed replacement/order semantics: %s", event.Data)
	}
	if string(got.Main.Entries[0]) != `{"seq":18446744073709551614,"text":"reinserted","exact":18446744073709551615}` || string(got.Main.Entries[1]) != `{"seq":3,"text":"complete replacement"}` {
		t.Fatalf("entry identity or raw values changed: %s", event.Data)
	}
}

func TestRPCSharedBacklogResetAndOmittedOrder(t *testing.T) {
	for _, reset := range []bool{false, true} {
		t.Run(fmt.Sprint(reset), func(t *testing.T) {
			events := []publication{{Kind: "patch", Data: jsontext.Value(`{"main":{"entries":[{"seq":1,"text":"first"}]}}`)}}
			if reset {
				events = append(events, publication{Kind: "snapshot", Data: jsontext.Value(`{"session":{"thread":"replacement"},"agents":{"entries":[{"seq":1,"text":"different feed"}],"order":[1]}}`)})
			}
			events = append(events, publication{Kind: "patch", Data: jsontext.Value(`{"main":{"entries":[{"seq":2,"text":"second"}]}}`)})
			_, subscription := bufferedRPCPublications(t, "session/subscribe", events)
			event, err := subscription.Next(t.Context())
			var got message
			if err != nil || json.Unmarshal(event.Data, &got) != nil || got.Main.Order != nil {
				t.Fatalf("omitted order changed: %s %v", event.Data, err)
			}
			want := 2
			if reset {
				want = 1
				if event.Kind != "snapshot" || got.Session.Thread != "replacement" || len(got.Agents.Entries) != 1 {
					t.Fatalf("snapshot reset was merged with obsolete identity: %s", event.Data)
				}
			}
			if len(got.Main.Entries) != want || strings.Contains(string(event.Data), `"first"`) == reset {
				t.Fatalf("reset retained or removed the wrong entries: %s", event.Data)
			}
		})
	}
}

func TestRPCSharedReplaceableBytesDoNotAccumulate(t *testing.T) {
	payload, err := json.Marshal(map[string]any{"status": map[string]string{"text": strings.Repeat("x", 1<<20)}})
	if err != nil {
		t.Fatal(err)
	}
	events := make([]publication, 70)
	for i := range events {
		events[i] = publication{Kind: "patch", Data: payload}
	}
	client, subscription := bufferedRPCPublications(t, "session/subscribe", events)
	client.mu.Lock()
	bytes := subscription.bytes
	failure := subscription.err
	client.mu.Unlock()
	if bytes > len(payload) || failure != nil {
		t.Fatalf("replaceable bytes accumulated: %d, %v", bytes, failure)
	}
	event, err := subscription.Next(t.Context())
	if err != nil || string(event.Data) != string(payload) {
		t.Fatalf("large progress did not reach consumer: size=%d err=%v", len(event.Data), err)
	}
}

func TestSharedServerBacklogCatchesUpThroughSubscription(t *testing.T) {
	server := NewLive()
	subscription, failure := server.subscribe(t.Context())
	if failure != nil {
		t.Fatal(failure)
	}
	defer subscription.close()
	fast, failure := server.subscribe(t.Context())
	if failure != nil {
		t.Fatal(failure)
	}
	defer fast.close()
	frame := Frame{Session: Session{Thread: "thread"}}
	for i := range subscriberBuffer*3 + 1 {
		entry := activity.Entry{Revision: uint64(i + 1), Values: []activity.Value{{Kind: "final", Body: fmt.Sprint(i)}}}
		entry.Seq, entry.Kind = 18446744073709551614, "final"
		frame.Main = &activity.Publication{Entries: []activity.Entry{entry}}
		if err := server.Publish(frame); err != nil {
			t.Fatal(err)
		}
		select {
		case event, open := <-fast.events:
			if !open || i > 0 && event.Kind != "patch" {
				t.Fatalf("slow consumer affected the fast subscriber: %s, open=%v", event.Kind, open)
			}
		default:
			t.Fatal("fast subscriber missed a publication")
		}
	}
	event, open := <-subscription.events
	var got message
	if !open || event.Kind != "snapshot" || json.Unmarshal(event.Data, &got) != nil || len(got.Main.Entries) != 1 || !strings.Contains(string(got.Main.Entries[0]), `"body":"192"`) {
		t.Fatalf("shared server did not catch up in place: %s, open=%v", event.Data, open)
	}
	frame.Session.Title = "still subscribed"
	if err := server.Publish(frame); err != nil {
		t.Fatal(err)
	}
	if next, open := <-subscription.events; !open || next.Kind != "patch" {
		t.Fatalf("catch-up retired the original subscription: %+v open=%v", next, open)
	}
}
