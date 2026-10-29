package control

import (
	"fmt"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/persistence"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func BenchmarkCheckpointPublicationGrowth(b *testing.B) {
	for _, count := range []int{100, 1000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			histories, err := history.NewService(RecordKinds())
			if err != nil {
				b.Fatal(err)
			}
			store, err := histories.Open(b.Context(), b.TempDir())
			if err != nil {
				b.Fatal(err)
			}
			selected, err := store.SelectSession(b.Context(), nil, history.Limits{}, "root")
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(selected.Close)
			owner := &treeOwner{session: selected}
			tree := &Tree{Version: checkpointVersion, Root: "root", Members: map[string]*Member{"root": {Thread: "root", Session: "root-session", Task: "/root"}}}
			for i := range count {
				tree.Effects = append(tree.Effects, Effect{ID: fmt.Sprint(i), Caller: "root", Target: "root", Kind: "message", State: "confirmed", Message: strings.Repeat("x", 4096)})
			}
			if err := owner.publish(b.Context(), tree); err != nil {
				b.Fatal(err)
			}
			writes := new(persistence.Counter)
			owner.session.Writes = writes
			b.ReportAllocs()
			for b.Loop() {
				next := *owner.state.Load()
				next.Members = map[string]*Member{"root": new(*next.Members["root"])}
				next.Members["root"].Generation++
				if err := owner.publish(b.Context(), &next); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(writes.Snapshot().Bytes)/float64(b.N), "write-B/op")
		})
	}
}
