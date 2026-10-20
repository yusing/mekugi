package sessioncontrol

import (
	"fmt"
	"strings"
	"testing"

	activity "github.com/yusing/mekugi/modules/activity/presentation"
)

func TestPreparedContentAudienceAndBounds(t *testing.T) {
	a := NewAudience(nil)
	a.Attach("root")
	a.Observe("child", "root", "/root/child", true)
	a.ObservePrepared("child", activity.PreparedContent{ID: "cell", Text: "actual projected output", Calls: []string{"one", "two"}, Cell: true})
	for _, id := range []string{"cell", "one", "two"} {
		got, ok := a.Prepared("root", "child", id)
		if !ok || got.ID != "cell" || !got.Cell || got.Text != "actual projected output" {
			t.Fatalf("%s: %+v", id, got)
		}
		got.Calls[0] = "mutated"
	}
	if _, ok := a.Prepared("other", "child", "one"); ok {
		t.Fatal("cross-root content")
	}
	a.Detach()
	if _, ok := a.Prepared("root", "child", "one"); ok {
		t.Fatal("detached content")
	}
	a.Attach("root")
	a.ObservePrepared("child", activity.PreparedContent{ID: "cell", Text: strings.Repeat("x", preparedContentBytes+1)})
	if _, ok := a.Prepared("root", "child", "one"); ok {
		t.Fatal("oversized replacement left stale content")
	}
	for i := range 257 {
		a.ObservePrepared("child", activity.PreparedContent{ID: fmt.Sprint(i), Text: strings.Repeat("x", 40000)})
	}
	if len(a.prepared) > 256 || a.preparedBytes > preparedTotalBytes {
		t.Fatal("unbounded retention")
	}
	if _, ok := a.Prepared("root", "child", "0"); ok {
		t.Fatal("old content not evicted")
	}
	if _, ok := a.Prepared("root", "child", "256"); !ok {
		t.Fatal("latest content missing")
	}
	a.Invalidate("child")
	if _, ok := a.Prepared("root", "child", "256"); ok {
		t.Fatal("invalidated identity retained access")
	}
	a.Close()
	if len(a.prepared) != 0 || a.preparedBytes != 0 {
		t.Fatal("close retained private content")
	}
}
