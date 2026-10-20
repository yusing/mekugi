package sessionpresentation

import (
	"fmt"
	"slices"
	"testing"
	"time"
)

func TestRestorationDiscoveryOrderAndCompletion(t *testing.T) {
	r := NewRestoration("main")
	accepted, notice := r.Discover([]string{"main", "child", "child", ""}, "next")
	if !slices.Equal(accepted, []int{1}) || notice != "" || r.View().Cursor != "next" {
		t.Fatalf("first page: %v %q %+v", accepted, notice, r.View())
	}
	r.Discover(nil, "")
	if !r.View().Archived || r.View().Listed {
		t.Fatal("archived discovery was omitted")
	}
	accepted, notice = r.Discover([]string{"child", "archived"}, "next")
	if !slices.Equal(accepted, []int{1}) || notice != "" {
		t.Fatal("archived cursor or identity was incorrectly reused")
	}
	r.Discover(nil, "")
	view := r.View()
	view.Threads[0] = "foreign"
	if !slices.Equal(r.View().Threads, []string{"child", "archived"}) || r.Ready() {
		t.Fatal("discovery published mutable or completed content")
	}
	r.ReadComplete()
	r.ReadComplete()
	if r.Ready() {
		t.Fatal("host reads bypassed content hydration")
	}
	r.ContentRendered()
	if !r.Ready() {
		t.Fatal("content hydration did not complete restoration")
	}
}

func TestRestorationDiscoveryBoundsKeepSuccessfulPrefix(t *testing.T) {
	for _, mode := range []string{"threads", "pages", "cursor", "failure"} {
		t.Run(mode, func(t *testing.T) {
			r := NewRestoration("main")
			var notice string
			switch mode {
			case "threads":
				ids := make([]string, RestoreThreadLimit+1)
				for i := range ids {
					ids[i] = fmt.Sprint(i)
				}
				accepted, message := r.Discover(ids, "")
				notice = message
				if len(accepted) != RestoreThreadLimit {
					t.Fatal("child limit lost its admitted prefix")
				}
			case "pages":
				for i := range 8 {
					_, notice = r.Discover([]string{fmt.Sprint(i)}, fmt.Sprint(i))
				}
			case "cursor":
				r.Discover([]string{"child"}, "same")
				_, notice = r.Discover(nil, "same")
			case "failure":
				r.Discover([]string{"child"}, "next")
				r.EndDiscovery()
			}
			if !r.View().Listed || len(r.View().Threads) == 0 || mode != "failure" && notice == "" {
				t.Fatalf("bounded discovery lost coverage: %+v %q", r.View(), notice)
			}
		})
	}
}

func TestHistoryPlacementAnchorsTimesAndDeferredPages(t *testing.T) {
	at := func(seconds int64) time.Time { return time.Unix(seconds, 0) }
	turns := []TurnPosition{{ID: "old", Started: at(10)}, {ID: "recent", Started: at(20), Items: []ItemPosition{{ID: "first", At: at(21), Known: true}, {ID: "last", At: at(25), Known: true}}}}
	placements := []Placement{{Turn: "recent", Anchor: "first", At: at(26)}, {At: at(22)}, {At: at(22)}, {Turn: "recent", Anchor: "absent", At: at(24)}}
	if got := turns[1].Slots(placements); len(got) != 3 || !slices.Equal(got[1], []int{1, 2, 3, 0}) {
		t.Fatalf("history slot order: %v", got)
	}
	if (Placement{Turn: "absent", At: at(22)}).TurnFor(turns) != -1 || (Placement{At: at(5)}).TurnFor(turns) != 0 {
		t.Fatal("turn identity or early-time fallback changed")
	}
	page := TurnPosition{ID: "recent", Started: at(20), Items: []ItemPosition{turns[1].Items[1]}}
	if placements[0].InPage(turns, page, 1, false) || placements[1].InPage(turns, page, 1, false) || !placements[0].InPage(turns, page, 1, true) {
		t.Fatal("unloaded evidence was inserted before its page")
	}
}
