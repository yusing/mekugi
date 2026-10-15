package composition

import (
	"strings"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/contracts/recordkind"
	"github.com/yusing/mekugi/modules/sessionhistory"
)

func TestCompositionCollectsRecordKindsBeforeHostConstruction(t *testing.T) {
	legacy := recordkind.Spec{ID: "legacy", Match: func(name string) bool { return name == "legacy.json" }}
	later := recordkind.Spec{ID: "later", Match: func(name string) bool { return name == "later.json" }, Keep: true}
	c := Composition{LegacyRecordKinds: []recordkind.Spec{legacy}}
	host := c.Add(sessionhistory.Module, func() sessionhistory.Deps { return sessionhistory.Deps{} })
	contributor := descriptor("later")
	contributor.Contributes.RecordKinds = []recordkind.Spec{later}
	contributor.New = func(_ struct{}, slots module.Slots) (*testService, error) {
		if len(slots.RecordKinds) != 0 {
			t.Fatal("non-host received record contributions")
		}
		return new(testService), nil
	}
	c.Add(contributor, emptyDeps)
	runtime, err := c.Activate(t.Context(), new(binding.Registry))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(t.Context()); err != nil {
			t.Error(err)
		}
	})
	for _, want := range []recordkind.Spec{legacy, later} {
		kind, found, err := host.Value().Kind(want.ID + ".json")
		if err != nil || !found || kind.ID != want.ID || kind.Keep != want.Keep {
			t.Fatalf("collected %s: kind=%+v found=%v err=%v", want.ID, kind, found, err)
		}
	}
}

func TestFrontendsExposesConstructedHistoryHost(t *testing.T) {
	runtime, err := Frontends(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(t.Context()); err != nil {
			t.Error(err)
		}
	})
	if runtime.History == nil {
		t.Fatal("application omitted the history host")
	}
	kind, found, err := runtime.History.Kind("call-" + strings.Repeat("a", 64) + ".json")
	if err != nil || !found || kind.ID != "call" || kind.Dependencies == nil {
		t.Fatalf("legacy call contribution: kind=%+v found=%v err=%v", kind, found, err)
	}
}
