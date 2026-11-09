package history

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/contracts/recordkind"
)

func TestAccountingPeerProcess(t *testing.T) {
	if directory := os.Getenv("MEKUGI_ACCOUNTING_TEST_DIRECTORY"); directory != "" {
		store := retentionFixtureStore(t, directory, recordkind.Spec{ID: "entry", Match: func(name string) bool { return strings.HasPrefix(name, "entry-") }})
		err := store.Transaction(t.Context(), nil, func(tx *Transaction) error {
			if os.Getenv("MEKUGI_ACCOUNTING_TEST_ACTION") == "remove-crash" {
				if err := tx.remove("entry-0"); err != nil {
					return err
				}
				os.Exit(0) // Real process exit before accounting commit/unlock.
			}
			if err := tx.Write("entry-0", "entry-pending-", []byte("changed")); err != nil {
				return err
			}
			if os.Getenv("MEKUGI_ACCOUNTING_TEST_ACTION") == "write-crash" {
				os.Exit(0)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	for _, action := range []string{"write", "write-crash", "remove-crash"} {
		t.Run(action, func(t *testing.T) {
			classifications := 0
			store := retentionFixtureStore(t, t.TempDir(), recordkind.Spec{ID: "entry", Match: func(name string) bool {
				classifications++
				return strings.HasPrefix(name, "entry-")
			}})
			if err := store.Transaction(t.Context(), nil, func(tx *Transaction) error {
				for i := range 128 {
					if err := tx.Write(fmt.Sprint("entry-", i), "entry-pending-", []byte("x")); err != nil {
						return err
					}
				}
				return tx.Admit(Limits{}, "entry-0", 1)
			}); err != nil {
				t.Fatal(err)
			}
			command := exec.Command(os.Args[0], "-test.run=^TestAccountingPeerProcess$")
			command.Env = append(os.Environ(), "MEKUGI_ACCOUNTING_TEST_DIRECTORY="+store.directory, "MEKUGI_ACCOUNTING_TEST_ACTION="+action)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("peer publication: %v\n%s", err, output)
			}
			classifications = 0
			peer := store.service.Attach(store.directory)
			used := int64(127 + len("changed"))
			if action == "remove-crash" {
				used = 127
			}
			if err := peer.Transaction(t.Context(), nil, func(tx *Transaction) error {
				if err := tx.Admit(Limits{Total: used + 1}, "entry-next", 1); err != nil {
					return err
				}
				err := tx.Admit(Limits{Total: used}, "entry-next", 1)
				var capacity *CapacityError
				if !errors.As(err, &capacity) || capacity.Required != used+1 {
					t.Fatalf("actual peer effects were not accounted: %v, want %d bytes", err, used+1)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if action == "write" && classifications > 8 || action != "write" && classifications < 128 {
				t.Fatalf("ordinary peer must not enumerate; interrupted peer must recover: %d classifications", classifications)
			}
			classifications = 0
			if err := store.service.Attach(store.directory).Transaction(t.Context(), nil, func(tx *Transaction) error {
				return tx.Admit(Limits{Total: used + 1}, "entry-next", 1)
			}); err != nil {
				t.Fatal(err)
			}
			if classifications > 4 {
				t.Fatal("completed recovery was repeated by the next peer")
			}
		})
	}
}

func TestAccountingRetirementCommitsPartialBatch(t *testing.T) {
	store, _ := pruneFixture(t, 1, 35, 4)
	interrupted := errors.New("retirement stopped after effects")
	err := store.Transaction(t.Context(), nil, func(tx *Transaction) error {
		if err := tx.remove("entry-0000"); err != nil {
			return err
		}
		if err := tx.Write("entry-0001", "entry-pending-", []byte("changed")); err != nil {
			return err
		}
		return interrupted
	})
	if !errors.Is(err, interrupted) {
		t.Fatal(err)
	}
	peer := store.service.Attach(store.directory)
	if err := peer.Transaction(t.Context(), nil, func(tx *Transaction) error {
		accounting, err := tx.accountingView(true)
		if err != nil {
			return err
		}
		files, err := tx.FileSizes()
		if err != nil {
			return err
		}
		want, _ := peer.Usage(Limits{}, files, "entry-next", 1)
		kind, _, _ := peer.service.Kind("entry-next")
		got, _ := peer.accountingUsage(accounting, kind).required(Limits{}, 0)
		if got+1 != want || files["entry-0000"] != 0 || files["entry-0001"] != 7 {
			t.Fatalf("partial retirement accounting: got %d want %d", got+1, want)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAccountingRebuildsWhenRecordOwnersChange(t *testing.T) {
	directory := t.TempDir()
	first := recordkind.Spec{ID: "entry", Match: func(name string) bool { return name == "entry" }}
	second := recordkind.Spec{ID: "other", Match: func(name string) bool { return name == "other" }, SeparateBudget: true, Limit: 4}
	store := retentionFixtureStore(t, directory, first)
	if err := os.WriteFile(filepath.Join(directory, "other"), []byte("1234"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.Transaction(t.Context(), nil, func(tx *Transaction) error {
		if err := tx.Write("entry", "entry-pending-", []byte("x")); err != nil {
			return err
		}
		return tx.Admit(Limits{}, "entry", 1)
	}); err != nil {
		t.Fatal(err)
	}
	peer := retentionFixtureStore(t, directory, first, second)
	if err := peer.Transaction(t.Context(), nil, func(tx *Transaction) error {
		if err := tx.Admit(Limits{Total: 1}, "entry", 1); err != nil {
			return err
		}
		if err := tx.Admit(Limits{}, "other", 5); err == nil {
			t.Fatal("new record owner's separate budget was not enforced")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
