package accounting

import (
	"testing"

	"github.com/yusing/mekugi/libraries/persistence"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

type usageTestStore struct {
	directory   string
	publication *history.Store
	writes      *persistence.Counter
}

func testHistory() *history.Service {
	s, err := history.NewService(RecordKinds())
	if err != nil {
		panic(err)
	}
	return s
}
func newTestUsage(t *testing.T) *Service {
	t.Helper()
	u := New(testHistory())
	if err := u.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(u.Close)
	return u
}
func usageStoreFixture(t *testing.T, directory string) *usageTestStore {
	t.Helper()
	s, err := testHistory().Open(t.Context(), directory)
	if err != nil {
		t.Fatal(err)
	}
	return &usageTestStore{directory: directory, publication: s}
}
func storedUsageFixture(t *testing.T, store *usageTestStore) *Service {
	t.Helper()
	u := newTestUsage(t)
	u.store, u.writes, u.limits = store.publication, store.writes, history.Limits{Total: 4 << 30}
	return u
}
