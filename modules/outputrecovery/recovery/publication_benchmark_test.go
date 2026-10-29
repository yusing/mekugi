package recovery

import (
	"fmt"
	"strings"
	"testing"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/libraries/persistence"
	"github.com/yusing/mekugi/modules/outputrecovery/internal/record"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func BenchmarkCursorPublicationGrowth(b *testing.B) {
	for _, count := range []int{100, 10000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			histories, err := history.NewService(RecordKinds())
			if err != nil {
				b.Fatal(err)
			}
			store, err := histories.Open(b.Context(), b.TempDir())
			if err != nil {
				b.Fatal(err)
			}
			selected, err := store.SelectSession(b.Context(), nil, DefaultLimits(), "root")
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(selected.Close)
			service, err := New(histories, nil)
			if err != nil {
				b.Fatal(err)
			}
			session, err := service.AttachStore(store, "root", "root", DefaultLimits(), nil)
			if err != nil {
				b.Fatal(err)
			}
			names := make([]string, count)
			for i := range names {
				names[i] = outputName("root", history.ShortHandle(uint64(i+100000)))
			}
			if err := store.Transaction(b.Context(), nil, func(tx *history.Transaction) error {
				return tx.Retain("root", DefaultLimits(), names...)
			}); err != nil {
				b.Fatal(err)
			}
			id, err := session.Publish(b.Context(), command.OmittedOutput{Stdout: strings.Repeat("x", 1<<20)}, 0)
			if err != nil {
				b.Fatal(err)
			}
			source, err := session.read(b.Context(), id, nil)
			if err != nil {
				b.Fatal(err)
			}
			binding := record.Binding(source.Record)
			writes := new(persistence.Counter)
			session.writes = writes
			position := 0
			b.ReportAllocs()
			for b.Loop() {
				position++
				if _, err := session.putCursor(b.Context(), source.Record, binding, [2]int{position, 0}, "stdout", nil); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(writes.Snapshot().Bytes)/float64(b.N), "write-B/op")
		})
	}
}
