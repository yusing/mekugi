package history_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/persistence"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

// Model dependency-first host publication batches with growing ownership. The
// ownership catalog grows on every retained payload, rather than measuring only
// an unchanged warm catalog. Bytes count application writes, not device I/O.
func BenchmarkPublicationGrowth(b *testing.B) {
	for _, owned := range []int{100, 10000} {
		for _, batch := range []int{1, 8} {
			for _, size := range []int{1024, 1 << 20} {
				b.Run(fmt.Sprintf("owned=%d/batch=%d/bytes=%d", owned, batch, size), func(b *testing.B) {
					store := fixtureStore(b.TempDir(), managedRecord)
					names := make([]string, owned)
					for i := range names {
						names[i] = fmt.Sprintf("record-%d.json", i)
					}
					if err := store.Transaction(b.Context(), nil, func(tx *history.Transaction) error {
						return tx.Retain("thread", history.Limits{}, names...)
					}); err != nil {
						b.Fatal(err)
					}
					writes := new(persistence.Counter)
					payload := []byte(strings.Repeat("x", size))
					sequence := owned
					b.ReportAllocs()
					for b.Loop() {
						if err := store.Transaction(b.Context(), writes, func(tx *history.Transaction) error {
							for range batch {
								name := fmt.Sprintf("record-%d.json", sequence)
								sequence++
								if err := tx.Retain("thread", history.Limits{}, name); err != nil {
									return err
								}
								if err := tx.Admit(history.Limits{}, name, int64(len(payload))); err != nil {
									return err
								}
								if err := tx.Write(name, "record-pending-", payload); err != nil {
									return err
								}
							}
							return nil
						}); err != nil {
							b.Fatal(err)
						}
					}
					b.ReportMetric(float64(writes.Snapshot().Bytes)/float64(b.N), "write-B/op")
				})
			}
		}
	}
}
