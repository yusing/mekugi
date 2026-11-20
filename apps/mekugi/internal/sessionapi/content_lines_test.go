package sessionapi

import (
	json "encoding/json/v2"
	"fmt"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/modules/livepreview/preview"
)

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestLiveMetadataPageAvoidsSourceTransferAndDecoding(t *testing.T) {
	s := NewLive()
	frame := Frame{Session: Session{Thread: "thread", Workspace: "/workspace"}, Attachment: 1}
	for index := range 60 {
		frame.Live = append(frame.Live, preview.Preview{ID: fmt.Sprint(index), Caller: "/root/worker", Thread: "thread", Workspace: "/workspace", Input: strings.Repeat("source\n", 25000), Files: []diffmodel.ReviewFile{{BeforePath: "a.go", AfterPath: "a.go", Diff: strings.Repeat("raw diff\n", 10000)}}})
	}
	if err := s.Publish(frame); err != nil {
		t.Fatal(err)
	}
	if len(s.livePreviews) <= 7<<20 {
		t.Fatal("fixture does not exceed helper response budget")
	}
	cursor := PageCursor{Attachment: s.readAttachment, Kind: "live", Revision: s.contentVersions["live"]}
	seen := 0
	for {
		value, failure := s.page(mustJSON(t, cursor))
		if failure != nil {
			t.Fatal(failure)
		}
		page := value.(PageResult)
		data := mustJSON(t, page)
		if len(data) > 32<<10 {
			t.Fatal("metadata page exceeded shared envelope budget")
		}
		for _, metadata := range page.Live {
			if metadata.ID != fmt.Sprint(seen) || metadata.Input != "" || len(metadata.Files) != 1 || metadata.Files[0].BeforePath != "a.go" || metadata.Files[0].Diff != "" {
				t.Fatal("metadata page lost identity or transferred raw source")
			}
			seen++
		}
		if page.Next == nil {
			break
		}
		cursor = *page.Next
	}
	if seen != len(frame.Live) {
		t.Fatal("live metadata enumeration incomplete")
	}
	selected := ContentReference{Attachment: s.readAttachment, Kind: "live", Revision: s.contentVersions["live"], Key: "42"}
	full, failure := s.content(mustJSON(t, selected))
	if failure != nil || len(full.(ContentResult).Live) != 1 || full.(ContentResult).Live[0].Input != frame.Live[42].Input {
		t.Fatal("selected read did not borrow complete original source")
	}
	// Cached metadata pages visit only their bounded window, not the raw strings.
	small := testing.AllocsPerRun(3, func() {
		_, _ = livePage(frame.Live[:1], s.readAttachment, selected.Revision, PageCursor{Attachment: s.readAttachment, Kind: "live", Revision: selected.Revision})
	})
	large := testing.AllocsPerRun(3, func() { _, _ = livePage(frame.Live, s.readAttachment, selected.Revision, cursor) })
	if large > small*55+20 {
		t.Fatalf("metadata work scaled with raw source: small=%v large=%v", small, large)
	}
	frame.Live = slices.Clone(frame.Live)
	frame.Live[0].Input += "later"
	if err := s.Publish(frame); err != nil {
		t.Fatal(err)
	}
	if _, failure := s.page(mustJSON(t, cursor)); failure == nil {
		t.Fatal("stale live metadata cursor admitted")
	}
}

func TestLiveMetadataCallerFilterPreservesSourceContinuation(t *testing.T) {
	api := NewLive()
	frame := Frame{Session: Session{Thread: "thread", Workspace: "/workspace"}, Attachment: 1}
	for index := range 80 {
		caller := "/root"
		if index%2 != 0 {
			caller = "/root/worker"
		}
		path := strings.Repeat("p", 400) + ".go"
		frame.Live = append(frame.Live, preview.Preview{ID: fmt.Sprint(index), Caller: caller, Thread: "thread", Input: strings.Repeat("source\n", 10000), Files: []diffmodel.ReviewFile{{BeforePath: path, AfterPath: path}}})
	}
	if err := api.Publish(frame); err != nil {
		t.Fatal(err)
	}
	hub := NewHub(t.Context(), nil)
	registerTestSession(hub, "thread", api, Summary{Thread: "thread", Workspace: "/workspace"})
	server := httptest.NewServer(Versioned(hub.Handler()))
	defer server.Close()
	rpc, err := OpenRPC(t.Context(), server.Client(), server.URL+RPCPath, "")
	if err != nil {
		t.Fatal(err)
	}
	defer rpc.Close()
	cursor := PageCursor{Attachment: api.readAttachment, Kind: "live", Revision: api.contentVersions["live"], Caller: "/root"}
	count, pages := 0, 0
	for {
		var page PageResult
		if err := rpc.Call(t.Context(), "session/page", RPCParams{Thread: "thread", Body: mustJSON(t, cursor)}, &page); err != nil {
			t.Fatal(err)
		}
		if len(mustJSON(t, page)) > 32<<10 {
			t.Fatal("caller-filtered metadata exceeded shared budget")
		}
		for _, metadata := range page.Live {
			if metadata.Caller != cursor.Caller || metadata.ID != fmt.Sprint(2*count) || metadata.Input != "" {
				t.Fatalf("caller-filtered source enumeration changed at %d: %+v", count, metadata)
			}
			count++
		}
		pages++
		if page.Next == nil {
			break
		}
		if page.Next.Caller != cursor.Caller || page.Next.Before <= cursor.Before {
			t.Fatalf("cursor lost caller or source boundary: %+v", page.Next)
		}
		cursor = *page.Next
	}
	if count != len(frame.Live)/2 || pages < 2 {
		t.Fatalf("filtered continuation incomplete or untested: previews=%d pages=%d", count, pages)
	}
}
