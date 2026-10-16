package binding

import (
	"testing"

	"github.com/yusing/mekugi/contracts/capability"
	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/contracts/scope"
)

func TestObservationRegistration(t *testing.T) {
	var registry Registry
	if result := registry.Capability(capability.Observation); result.State != capability.Available {
		t.Fatalf("capability: %+v", result)
	}
	if err := registry.Observation(Observation{}); err == nil {
		t.Fatal("accepted missing factory")
	}
	binding := Observation{NewTranscript: func(*scope.Store) observation.Transcript { return nil }}
	if err := registry.Observation(binding); err != nil {
		t.Fatal(err)
	}
	if err := registry.Observation(binding); err == nil {
		t.Fatal("accepted duplicate observation owner")
	}
}
