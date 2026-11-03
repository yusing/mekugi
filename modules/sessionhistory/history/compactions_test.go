package history

import (
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"testing"

	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/contracts/recordkind"
)

func TestCompactionObservationIdentityAndUnknowns(t *testing.T) {
	service, err := NewService([]recordkind.Spec{CompactionRecord})
	if err != nil {
		t.Fatal(err)
	}
	store, err := service.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(store.Directory(), CompactionName("main", "item"))
	for _, test := range []struct {
		name    string
		record  CompactionObservation
		invalid bool
	}{
		{name: "unknown", record: CompactionObservation{Thread: "main", Item: "item"}},
		{name: "zero", record: CompactionObservation{Thread: "main", Item: "item", Context: &observation.Context{Known: true}, ElapsedNS: new(int64(0))}},
		{name: "foreign", record: CompactionObservation{Thread: "other", Item: "item"}, invalid: true},
		{name: "negative", record: CompactionObservation{Thread: "main", Item: "item", ElapsedNS: new(int64(-1))}, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			data, err := json.Marshal(test.record)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(name, data, 0600); err != nil {
				t.Fatal(err)
			}
			got, size, err := store.ReadCompaction("main", "item")
			if (err != nil) != test.invalid || size != len(data) {
				t.Fatalf("record=%+v bytes=%d err=%v", got, size, err)
			}
			if !test.invalid && ((got.Context == nil) != (test.record.Context == nil) || (got.ElapsedNS == nil) != (test.record.ElapsedNS == nil)) {
				t.Fatalf("unknown and zero conflated: %+v", got)
			}
		})
	}
}
