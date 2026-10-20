package launch

import (
	"testing"

	"github.com/yusing/mekugi/modules/usage/accounting"
)

func newTestUsage(t *testing.T) *accounting.Service {
	t.Helper()
	u := accounting.New(testHistory())
	if err := u.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(u.Close)
	return u
}
func usageStoreFixture(t *testing.T, directory string) *mekugiReplayStore {
	t.Helper()
	store, err := openMekugiReplayStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.snapshots.Close)
	return store
}
func storedUsageFixture(t *testing.T, store *mekugiReplayStore) *accounting.Service {
	t.Helper()
	u := newTestUsage(t)
	u.Configure(store.directory, store.writes, store.retentionLimits(), nil)
	return u
}
