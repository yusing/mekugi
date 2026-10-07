package router

import (
	"reflect"
	"testing"
)

func TestNativeForkHistorySelectionRestartAndIsolation(t *testing.T) {
	binding := ObservationBinding{Runtime: "claude", Session: "fork", Workspace: t.TempDir()}
	source := binding
	source.Session = "parent"
	directory := t.TempDir()
	service, _, closeService := observationIsolationService(t, directory, binding)
	children := []nativeHistoryChild{{SessionID: source.Session, AgentID: "native-child", MessageIDs: []string{"native-message-a", "native-message-b"}}}
	if err := service.owner.retainForkHistory(t.Context(), binding, source, children); err == nil {
		t.Fatal("unbound fork acquired retained history")
	}
	if err := service.owner.bind(t.Context(), binding); err != nil {
		t.Fatal(err)
	}
	if err := service.owner.retainForkHistory(t.Context(), binding, source, children); err != nil {
		t.Fatal(err)
	}
	changed := []nativeHistoryChild{{SessionID: source.Session, AgentID: "native-child", MessageIDs: []string{"later-message"}}}
	if err := service.owner.retainForkHistory(t.Context(), binding, source, changed); err == nil {
		t.Fatal("fork selection changed after publication")
	}
	closeService()
	fresh, _, _ := observationIsolationService(t, directory, binding)
	got, err := fresh.owner.readForkHistory(t.Context(), binding)
	if err != nil || !reflect.DeepEqual(got, children) || len(fresh.owner.bindings) != 0 {
		t.Fatalf("fresh selection changed or revived callers: %+v %v", got, err)
	}
	for _, other := range []ObservationBinding{
		{Runtime: "claude", Session: "other", Workspace: binding.Workspace},
		{Runtime: "claude", Session: binding.Session, Workspace: t.TempDir()},
	} {
		if got, err := fresh.owner.readForkHistory(t.Context(), other); err != nil || len(got) != 0 {
			t.Fatalf("unrelated scope borrowed selections: %+v %v", got, err)
		}
	}
}
