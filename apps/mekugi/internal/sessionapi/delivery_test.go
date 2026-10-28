package sessionapi

import (
	"encoding/json/v2"
	"fmt"
	"testing"
	"testing/synctest"
)

func TestSharedDeliveryCreditSurvivesSubscriptionCancellation(t *testing.T) {
	hub := NewHub(t.Context(), nil)
	server := New()
	registerTestSession(hub, "thread", server, Summary{Thread: "thread", Workspace: "/workspace"})
	frame := Frame{Session: Session{Thread: "thread", Title: "initial"}}
	if err := server.Publish(frame); err != nil {
		t.Fatal(err)
	}
	conn := rawControl(t, hub.Handler(), false)
	controlInitialize(t, conn)
	controlWrite(t, conn, `{"jsonrpc":"2.0","id":"first","method":"session/subscribe","params":{"thread":"thread","subscription":"old"}}`)
	if reply := controlRead(t, conn); string(reply.ID) != `"first"` {
		t.Fatal("publication preceded subscription acknowledgement")
	}
	var first RPCEvent
	if err := json.Unmarshal(controlRead(t, conn).Params, &first); err != nil || first.Delivery == "" {
		t.Fatalf("missing credit: %v", err)
	}
	for i := range 100 {
		frame.Session.Title = fmt.Sprintf("latest-%d", i)
		if err := server.Publish(frame); err != nil {
			t.Fatal(err)
		}
	}
	server.mu.Lock()
	for queued := range server.subscribers {
		if len(queued) != 1 {
			t.Error("progress did not coalesce to one pending publication")
		}
	}
	server.mu.Unlock()
	controlWrite(t, conn, `{"jsonrpc":"2.0","id":"control","method":"session/read","params":{"thread":"thread"}}`)
	if reply := controlRead(t, conn); string(reply.ID) != `"control"` || reply.Error != nil {
		t.Fatal("unacknowledged history blocked control or released more data")
	}
	controlWrite(t, conn, `{"jsonrpc":"2.0","id":"cancel","method":"subscription/cancel","params":{"subscription":"old"}}`)
	for range 2 {
		reply := controlRead(t, conn)
		if reply.Method != "subscription/closed" && string(reply.ID) != `"cancel"` {
			t.Fatal("cancellation released sent credit")
		}
	}
	controlWrite(t, conn, `{"jsonrpc":"2.0","id":"second","method":"session/subscribe","params":{"thread":"thread","subscription":"new"}}`)
	if reply := controlRead(t, conn); string(reply.ID) != `"second"` {
		t.Fatal("new subscription was blocked by the old delivery")
	}
	controlWrite(t, conn, `{"jsonrpc":"2.0","id":"wrong","method":"publication/ack","params":{"body":{"delivery":"wrong"}}}`)
	if reply := controlRead(t, conn); string(reply.ID) != `"wrong"` {
		t.Fatal("wrong receipt released a delivery")
	}
	controlWrite(t, conn, `{"jsonrpc":"2.0","id":"ack","method":"publication/ack","params":{"body":{"delivery":"`+first.Delivery+`"}}}`)
	var next RPCEvent
	for range 2 {
		reply := controlRead(t, conn)
		if reply.Method == "subscription/event" {
			if err := json.Unmarshal(reply.Params, &next); err != nil {
				t.Fatal(err)
			}
		} else if string(reply.ID) != `"ack"` {
			t.Fatalf("unexpected reply: %+v", reply)
		}
	}
	var current message
	if err := json.Unmarshal(next.Data, &current); err != nil || next.Subscription != "new" || next.Delivery == first.Delivery || current.Session.Title != "latest-99" {
		t.Fatalf("replacement did not receive authoritative bounded state: %+v, %v", next, err)
	}
}

func TestSharedPagePriorityAndCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hub, server := NewHub(t.Context(), nil), New()
		registerTestSession(hub, "thread", server, Summary{Thread: "thread", Workspace: "/workspace"})
		frame := Frame{Session: Session{Thread: "thread", Title: "initial"}}
		if err := server.Publish(frame); err != nil {
			t.Fatal(err)
		}
		conn := pipeControl(t, hub)
		controlInitialize(t, conn)
		controlWrite(t, conn, `{"jsonrpc":"2.0","id":"subscribe","method":"session/subscribe","params":{"thread":"thread","subscription":"shared"}}`)
		controlRead(t, conn)
		var first RPCEvent
		if err := json.Unmarshal(controlRead(t, conn).Params, &first); err != nil {
			t.Fatal(err)
		}
		cursor := string(testJSON(t, PageCursor{Attachment: server.readAttachment, Kind: "main", Revision: server.main.history}))
		controlWrite(t, conn, `{"jsonrpc":"2.0","id":"obsolete","method":"session/page","params":{"thread":"thread","body":`+cursor+`}}`)
		synctest.Wait()
		controlWrite(t, conn, `{"jsonrpc":"2.0","id":"cancel","method":"request/cancel","params":{"body":{"id":"obsolete"}}}`)
		if reply := controlRead(t, conn); string(reply.ID) != `"cancel"` {
			t.Fatal("canceled page was delivered")
		}
		synctest.Wait()
		hub.mu.Lock()
		for connection := range hub.connections {
			connection.mu.Lock()
			if connection.page != nil || connection.delivery != first.Delivery {
				t.Error("cancel did not remove only unsent page work")
			}
			connection.mu.Unlock()
		}
		hub.mu.Unlock()
		frame.Session.Title = "latest"
		if err := server.Publish(frame); err != nil {
			t.Fatal(err)
		}
		controlWrite(t, conn, `{"jsonrpc":"2.0","id":"page","method":"session/page","params":{"thread":"thread","body":`+cursor+`}}`)
		synctest.Wait()
		controlWrite(t, conn, `{"jsonrpc":"2.0","id":"ack","method":"publication/ack","params":{"body":{"delivery":"`+first.Delivery+`"}}}`)
		var page PageResult
		for range 2 {
			reply := controlRead(t, conn)
			if string(reply.ID) == `"page"` {
				if err := json.Unmarshal(reply.Result, &page); err != nil {
					t.Fatal(err)
				}
			} else if string(reply.ID) != `"ack"` {
				t.Fatal("replaceable progress overtook a pending page")
			}
		}
		if page.Delivery == "" || page.Delivery == first.Delivery {
			t.Fatal("page did not own one fresh delivery credit")
		}
		controlWrite(t, conn, `{"jsonrpc":"2.0","id":"read","method":"session/read","params":{"thread":"thread"}}`)
		if reply := controlRead(t, conn); string(reply.ID) != `"read"` {
			t.Fatal("page credit blocked control or admitted another data window")
		}
		controlWrite(t, conn, `{"jsonrpc":"2.0","id":"page-ack","method":"publication/ack","params":{"body":{"delivery":"`+page.Delivery+`"}}}`)
		var next RPCEvent
		for range 2 {
			reply := controlRead(t, conn)
			if reply.Method == "subscription/event" {
				if err := json.Unmarshal(reply.Params, &next); err != nil {
					t.Fatal(err)
				}
			} else if string(reply.ID) != `"page-ack"` {
				t.Fatal("unexpected delivery after page application")
			}
		}
		var current message
		if err := json.Unmarshal(next.Data, &current); err != nil || current.Session == nil || current.Session.Title != "latest" {
			t.Fatal("page receipt lost pending current publication")
		}
	})
}
