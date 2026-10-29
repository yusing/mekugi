package retained

import (
	"fmt"
	"testing"

	"github.com/yusing/mekugi/libraries/persistence"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func BenchmarkChangeIndexPublicationGrowth(b *testing.B) {
	for _, count := range []int{100, 10000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			histories, err := history.NewService(RecordKinds())
			if err != nil {
				b.Fatal(err)
			}
			publication, err := histories.Open(b.Context(), b.TempDir())
			if err != nil {
				b.Fatal(err)
			}
			selected, err := publication.SelectSession(b.Context(), nil, history.Limits{}, "root")
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(selected.Close)
			workspace := b.TempDir()
			s := selectedStore(changes.Scope{Store: publication, Namespace: "root", Thread: "root"})
			index := changeIndex{Version: 1, Workspace: workspace, Namespace: "root", Streams: []changeStream{{Name: "amber", Thread: "root", Next: count}}, Changes: make(map[string]trackedChange, count)}
			for i := range count {
				index.Changes[changes.ID("amber", i+1)] = trackedChange{Correlation: fmt.Sprint(i)}
			}
			if err := s.locked(b.Context(), func(store *store) error { return store.writeChangeIndex(index) }); err != nil {
				b.Fatal(err)
			}
			writes := new(persistence.Counter)
			s.writes = writes
			sequence := count
			b.ReportAllocs()
			for b.Loop() {
				sequence++
				if _, err := s.reserveChange(b.Context(), workspace, "root", fmt.Sprint(sequence)); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(writes.Snapshot().Bytes)/float64(b.N), "write-B/op")
		})
	}
}
