package sessionhistory_test

import (
	"testing"

	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/contracts/recordkind"
	"github.com/yusing/mekugi/modules/sessionhistory"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

func TestModuleBackendNeutralRecordHost(t *testing.T) {
	for _, contributed := range []bool{false, true} {
		name := "empty"
		var kinds []recordkind.Spec
		if contributed {
			name = "contributed"
			kinds = []recordkind.Spec{{ID: "example", Match: func(name string) bool { return name == "example.json" }, Keep: true}}
		}
		t.Run(name, func(t *testing.T) {
			// No scripted backend is needed to construct the record host.
			service := moduletest.New(t, sessionhistory.Module, sessionhistory.Deps{}, module.Slots{RecordKinds: kinds}, nil)
			kind, found, err := service.Kind("example.json")
			if err != nil || found != contributed {
				t.Fatalf("recognition: kind=%+v found=%v err=%v", kind, found, err)
			}
			if contributed && (kind.ID != "example" || !kind.Keep) {
				t.Fatalf("contribution lost: %+v", kind)
			}
			if _, found, err := service.Kind("unmanaged.json"); err != nil || found {
				t.Fatalf("unmanaged record: found=%v err=%v", found, err)
			}
		})
	}
}

func TestModuleRejectsDuplicateAndAmbiguousRecordKinds(t *testing.T) {
	match := func(name string) bool { return name == "shared.json" }
	if _, err := sessionhistory.Module.New(sessionhistory.Deps{}, module.Slots{RecordKinds: []recordkind.Spec{
		{ID: "same", Match: match}, {ID: "same", Match: match},
	}}); err == nil {
		t.Fatal("duplicate kind identity accepted")
	}
	service := moduletest.New(t, sessionhistory.Module, sessionhistory.Deps{}, module.Slots{RecordKinds: []recordkind.Spec{
		{ID: "first", Match: match}, {ID: "second", Match: match},
	}}, nil)
	if _, _, err := service.Kind("shared.json"); err == nil {
		t.Fatal("ambiguous recognition accepted")
	}
}
