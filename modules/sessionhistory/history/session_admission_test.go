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

func TestSessionMembershipSurvivesRestartAndRetention(t *testing.T) {
	store := retentionFixtureStore(t, t.TempDir())
	if found, err := store.ContainsSession("stock"); err != nil || found {
		t.Fatalf("missing session membership: %v %v", found, err)
	}
	_, release, err := store.BeginSession(t.Context(), nil, Limits{}, "fresh")
	if err != nil {
		t.Fatal(err)
	}
	release()
	if err := store.Transaction(t.Context(), nil, func(tx *Transaction) error {
		return tx.InitializeScope(Scope{Thread: "fresh", Namespace: "fresh"}, nil)
	}); err != nil {
		t.Fatal(err)
	}
	store = retentionFixtureStore(t, store.Directory())
	if found, err := store.ContainsSession("fresh"); err != nil || !found {
		t.Fatalf("session before first turn lost enrollment: %v %v", found, err)
	}
	ageRetentionCatalog(t, store, "fresh")
	if err := store.Cleanup(t.Context(), nil, Limits{}, "", nil); err != nil {
		t.Fatal(err)
	}
	requireRetentionFiles(t, store.Directory(), false, SessionName("fresh"))
	if found, err := store.ContainsSession("fresh"); err != nil || !found {
		t.Fatalf("retention erased membership: %v %v", found, err)
	}
	if err := os.WriteFile(filepath.Join(store.Directory(), ScopeName("fresh")), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ContainsSession("fresh"); err == nil {
		t.Fatal("corrupt membership was ignored")
	}
}
