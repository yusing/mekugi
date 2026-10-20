package executionobservation

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
