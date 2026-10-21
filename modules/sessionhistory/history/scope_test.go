package history_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func scopeStore(directory string) *history.Store {
	return fixtureStore(directory, func(name string) bool {
		return strings.HasPrefix(name, "handle-scope-")
	})
}

func TestScopeWireCompatibility(t *testing.T) {
	directory := t.TempDir()
	scope := history.Scope{Version: 1, Thread: "branch", Namespace: "branch", Parent: "root", Fork: "child", NextChangeStream: 2, Next: 8, Inherited: []history.Range{{Namespace: "root", End: 3}}}
	const name = "handle-scope-f38c764c8aa00b6578f4254a4dc6d9b50f88fa926e270ea7859bd1b707cd8662"
	const wire = `{"Version":1,"Thread":"branch","Namespace":"branch","Parent":"root","Fork":"child","NextChangeStream":2,"Next":8,"Inherited":[{"Namespace":"root","End":3}]}`
	if err := scopeStore(directory).Transaction(t.Context(), nil, func(tx *history.Transaction) error {
		return tx.WriteScope(scope)
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(directory, name))
	if err != nil || string(data) != wire {
		t.Fatalf("retained wire=%s err=%v", data, err)
	}
	got, found, err := scopeStore(directory).ReadScope("branch")
	if err != nil || !found || !reflect.DeepEqual(got, scope) {
		t.Fatalf("fresh reader: scope=%+v found=%v err=%v", got, found, err)
	}
	if err := scopeStore(directory).Transaction(t.Context(), nil, func(tx *history.Transaction) error {
		return tx.WriteScope(history.Scope{Version: 1, Thread: "branch", Namespace: "branch"})
	}); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(directory, name))
	if err != nil || string(data) != `{"Version":1,"Thread":"branch","Namespace":"branch","Next":0}` {
		t.Fatalf("zero-value wire=%s err=%v", data, err)
	}
}

func TestScopeFreshStoreRestoresNamespaceAndInheritedAllocation(t *testing.T) {
	directory := t.TempDir()
	store := scopeStore(directory)
	if err := store.Transaction(t.Context(), nil, func(tx *history.Transaction) error {
		for _, scope := range []history.Scope{
			{Version: 1, Thread: "root", Namespace: "root", Next: 3},
			{Version: 1, Thread: "child", Namespace: "root", Parent: "root"},
			{Version: 1, Thread: "branch", Namespace: "branch", Parent: "root", Fork: "child", Next: 3, Inherited: []history.Range{{Namespace: "root", End: 3}}},
		} {
			if err := tx.WriteScope(scope); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	reopened := scopeStore(directory)
	for thread, want := range map[string]string{"root": "root", "child": "root", "branch": "branch", "fresh": "fresh"} {
		got, err := reopened.NamespaceForThread(thread)
		if err != nil || got != want {
			t.Fatalf("namespace for %s=%q err=%v", thread, got, err)
		}
	}
	if err := reopened.Transaction(t.Context(), nil, func(tx *history.Transaction) error {
		if _, err := tx.ReserveHandles("child", 1); err == nil {
			t.Fatal("alias allocated outside its root namespace")
		}
		ids, err := tx.ReserveHandles("branch", 1)
		if err == nil && (len(ids) != 1 || ids[0] != history.ShortHandle(3)) {
			t.Fatalf("inherited allocation reused ancestor range: %v", ids)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	root, _, err := reopened.ReadScope("root")
	if err != nil || root.Next != 3 {
		t.Fatalf("branch changed ancestor allocation: %+v err=%v", root, err)
	}
	branch, found, err := scopeStore(directory).ReadScope("branch")
	if err != nil || !found || branch.Next != 4 || !reflect.DeepEqual(branch.Inherited, []history.Range{{Namespace: "root", End: 3}}) {
		t.Fatalf("branch lost inherited ownership: %+v found=%v err=%v", branch, found, err)
	}
	if err := os.Remove(filepath.Join(directory, history.ScopeName("root"))); err != nil {
		t.Fatal(err)
	}
	if _, err := scopeStore(directory).NamespaceForThread("child"); err == nil {
		t.Fatal("restored alias without its durable root")
	}
}

func TestScopeRejectsInvalidRetainedIdentityAndRanges(t *testing.T) {
	for name, wire := range map[string]string{
		"wrong thread":            `{"Version":1,"Thread":"other","Namespace":"branch","Next":0}`,
		"self parent":             `{"Version":1,"Thread":"branch","Namespace":"branch","Parent":"branch","Next":0}`,
		"alias allocation":        `{"Version":1,"Thread":"branch","Namespace":"root","Parent":"root","Next":1}`,
		"duplicate ancestor":      `{"Version":1,"Thread":"branch","Namespace":"branch","Next":4,"Inherited":[{"Namespace":"root","End":2},{"Namespace":"root","End":4}]}`,
		"decreasing ranges":       `{"Version":1,"Thread":"branch","Namespace":"branch","Next":4,"Inherited":[{"Namespace":"root","End":3},{"Namespace":"other","End":2}]}`,
		"range beyond allocation": `{"Version":1,"Thread":"branch","Namespace":"branch","Next":2,"Inherited":[{"Namespace":"root","End":3}]}`,
		"unknown field":           `{"Version":1,"Thread":"branch","Namespace":"branch","Next":0,"Future":true}`,
		"corrupt":                 `{`,
	} {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			if err := os.WriteFile(filepath.Join(directory, history.ScopeName("branch")), []byte(wire), 0600); err != nil {
				t.Fatal(err)
			}
			store := scopeStore(directory)
			if _, found, err := store.ReadScope("branch"); err == nil || found {
				t.Fatalf("invalid scope accepted: found=%v err=%v", found, err)
			}
			if err := store.Transaction(t.Context(), nil, func(tx *history.Transaction) error {
				_, err := tx.ReserveHandles("branch", 1)
				return err
			}); err == nil {
				t.Fatal("invalid allocation scope silently reset")
			}
		})
	}
}

func TestScopeNamespaceDiscoveryPreservesValidation(t *testing.T) {
	directory := t.TempDir()
	store := scopeStore(directory)
	for _, tc := range []struct {
		name, wire, namespace string
		belongs, invalid      bool
	}{
		{"unrelated feature", `{"Version":1,"Thread":"branch","Namespace":"branch","Coordinator":{"main":"branch"},"Next":0}`, "other", false, false},
		{"matching feature", `{"Version":1,"Thread":"branch","Namespace":"branch","Coordinator":{"main":"branch"},"Next":0}`, "branch", false, true},
		{"matching alias", `{"Version":1,"Thread":"branch","Namespace":"root","Parent":"root","Next":0}`, "root", true, false},
		{"invalid alias", `{"Version":1,"Thread":"branch","Namespace":"root","Parent":"root","Next":1}`, "root", false, true},
		{"wrong identity", `{"Version":1,"Thread":"other","Namespace":"other","Next":0}`, "root", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(directory, history.ScopeName("branch")), []byte(tc.wire), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := store.ScopeInNamespace("branch", tc.namespace)
			if got != tc.belongs || (err != nil) != tc.invalid {
				t.Fatalf("membership=%v err=%v", got, err)
			}
			data, err := os.ReadFile(filepath.Join(directory, history.ScopeName("branch")))
			if err != nil || string(data) != tc.wire {
				t.Fatal("discovery changed retained scope")
			}
		})
	}
}

func TestScopeReservationsSurviveFailureAndReopen(t *testing.T) {
	directory := t.TempDir()
	failure := errors.New("dependent publication failed")
	if err := scopeStore(directory).Transaction(t.Context(), nil, func(tx *history.Transaction) error {
		ids, err := tx.ReserveHandles("root", 2)
		if err != nil {
			return err
		}
		if len(ids) != 2 || ids[0] != history.ShortHandle(0) || ids[1] != history.ShortHandle(1) {
			t.Fatalf("initial reservation=%v", ids)
		}
		return failure
	}); !errors.Is(err, failure) {
		t.Fatalf("publication error=%v", err)
	}
	if err := scopeStore(directory).Transaction(t.Context(), nil, func(tx *history.Transaction) error {
		ids, err := tx.ReserveHandles("root", 1)
		if err == nil && (len(ids) != 1 || ids[0] != history.ShortHandle(2)) {
			t.Fatalf("failed publication recycled handles: %v", ids)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestScopeEscapedTransactionRejectsPublication(t *testing.T) {
	directory := t.TempDir()
	var escaped *history.Transaction
	if err := scopeStore(directory).Transaction(t.Context(), nil, func(tx *history.Transaction) error {
		escaped = tx
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if ids, err := escaped.ReserveHandles("root", 1); err == nil || len(ids) != 0 {
		t.Fatalf("escaped reservation=%v err=%v", ids, err)
	}
	if err := escaped.WriteScope(history.Scope{Version: 1, Thread: "root", Namespace: "root"}); err == nil {
		t.Fatal("escaped scope publication succeeded")
	}
	if _, found, err := scopeStore(directory).ReadScope("root"); err != nil || found {
		t.Fatalf("escaped transaction left retained scope: found=%v err=%v", found, err)
	}
}
