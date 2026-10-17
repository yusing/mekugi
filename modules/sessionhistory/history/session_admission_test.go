package history

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBeginSessionRestoresNamespaceAndHoldsOwnerLease(t *testing.T) {
	store := retentionFixtureStore(t, t.TempDir())
	if err := store.Transaction(t.Context(), nil, func(tx *Transaction) error {
		if err := tx.WriteScope(Scope{Version: 1, Thread: "root", Namespace: "root"}); err != nil {
			return err
		}
		return tx.WriteScope(Scope{Version: 1, Thread: "child", Namespace: "root", Parent: "root"})
	}); err != nil {
		t.Fatal(err)
	}
	namespace, release, err := store.BeginSession(t.Context(), nil, Limits{Total: DefaultStorageBytes}, "child")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if namespace != "root" {
		t.Fatalf("retained namespace = %q", namespace)
	}
	if _, err := os.Stat(filepath.Join(store.Directory(), SessionName("child"))); err != nil {
		t.Fatalf("active owner was not enrolled: %v", err)
	}
	name := strings.TrimSuffix(SessionName("child"), ".json") + ".lock"
	acquired, unlock, err := store.TryPruneLease(name)
	if acquired {
		unlock()
	}
	if err != nil || acquired {
		t.Fatalf("active owner was prunable: acquired=%v err=%v", acquired, err)
	}
	release()
	acquired, unlock, err = store.TryPruneLease(name)
	if err != nil || !acquired {
		t.Fatalf("retired owner retained its lease: acquired=%v err=%v", acquired, err)
	}
	unlock()
}

func TestBeginSessionFailedEnrollmentReleasesLease(t *testing.T) {
	store := retentionFixtureStore(t, t.TempDir())
	if err := os.Mkdir(filepath.Join(store.Directory(), SessionName("broken")), 0700); err != nil {
		t.Fatal(err)
	}
	_, _, err := store.BeginSession(t.Context(), nil, Limits{Total: DefaultStorageBytes}, "broken")
	if _, ok := errors.AsType[*PublicationError](err); !ok {
		t.Fatalf("enrollment failure lost publication classification: %v", err)
	}
	name := strings.TrimSuffix(SessionName("broken"), ".json") + ".lock"
	acquired, release, err := store.TryPruneLease(name)
	if err != nil || !acquired {
		t.Fatalf("failed enrollment retained its lease: acquired=%v err=%v", acquired, err)
	}
	release()
}
