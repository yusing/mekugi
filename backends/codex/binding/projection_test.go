package binding

import "testing"

func TestInputProjectionRegistration(t *testing.T) {
	registry := new(Registry)
	prepares := 0
	prepare := func([]byte) PreparedInputProjection {
		prepares++
		return func(input []byte) ProjectionResult { return ProjectionResult{Input: input} }
	}
	for _, invalid := range []InputProjection{{}, {Name: "missing-prepare"}, {Prepare: prepare}} {
		if err := registry.InputProjection(invalid); err == nil {
			t.Fatalf("accepted invalid projection %+v", invalid)
		}
	}
	for _, name := range []string{"first", "second"} {
		if err := registry.InputProjection(InputProjection{Name: name, Prepare: prepare}); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.InputProjection(InputProjection{Name: "first", Prepare: prepare}); err == nil {
		t.Fatal("accepted duplicate projection")
	}
	projections := registry.InputProjections()
	if len(projections) != 2 || projections[0].Name != "first" || projections[1].Name != "second" || prepares != 0 {
		t.Fatalf("registration = %+v, prepares = %d", projections, prepares)
	}
	projections[0] = InputProjection{}
	if got := registry.InputProjections(); len(got) != 2 || got[0].Name != "first" {
		t.Fatalf("caller mutated registrations: %+v", got)
	}
}
