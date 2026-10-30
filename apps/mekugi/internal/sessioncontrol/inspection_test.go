package sessioncontrol

import (
	"testing"
	"testing/synctest"

	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestInspectionUsesLocalRuntimeHandoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		transport := &requestTransport{}
		requests := NewRequests[struct{}](transport)
		for _, target := range []string{"root", "foreign"} {
			done := make(chan error, 1)
			go func() {
				got, err := requests.Inspect(t.Context(), target)
				if target == "root" && ((got.Model == nil || *got.Model != "actual") || got.ContextTokens != nil || got.Compactions == nil || *got.Compactions != 0) {
					t.Errorf("snapshot = %+v", got)
				}
				done <- err
			}()
			call := <-requests.Inspections()
			call.Complete("root", control.SessionDetails{Model: new("actual"), Compactions: new(uint64(0))})
			if err := <-done; (err != nil) != (target == "foreign") {
				t.Fatalf("target %s: %v", target, err)
			}
		}
		if requests.Pending() || len(transport.sends) != 0 {
			t.Fatal("inspection dispatched a host operation")
		}
	})
}
