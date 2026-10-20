package history_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/contracts/recordkind"

	"github.com/yusing/mekugi/libraries/persistence"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func managedRecord(name string) bool { return strings.HasSuffix(name, ".json") }

func TestStoreIdenticalWriteAndPeerRefresh(t *testing.T) {
	directory := t.TempDir()
	store := fixtureStore(directory, managedRecord)
	peer := fixtureStore(directory, managedRecord)
	writes := new(persistence.Counter)
	var revision string
	if err := store.Transaction(t.Context(), writes, func(tx *history.Transaction) error {
		if err := tx.Write("record.json", "record-pending-", []byte("old")); err != nil {
			return err
		}
		var err error
		revision, err = tx.Revision()
		if err != nil {
			return err
		}
		_, err = tx.FileSizes()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	before := writes.Snapshot().Bytes
	if revision == "" || before <= 3 {
		t.Fatalf("publication missing revision/accounting: revision=%q bytes=%d", revision, before)
	}
	if err := store.Transaction(t.Context(), writes, func(tx *history.Transaction) error {
		if err := tx.Write("record.json", "record-pending-", []byte("old")); err != nil {
			return err
		}
		got, err := tx.Revision()
		if got != revision || writes.Snapshot().Bytes != before {
			t.Fatalf("identical write changed revision/accounting: revision=%q bytes=%d", got, writes.Snapshot().Bytes)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := peer.Transaction(t.Context(), writes, func(tx *history.Transaction) error {
		return tx.Write("record.json", "record-pending-", []byte("peer publication"))
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Transaction(t.Context(), nil, func(tx *history.Transaction) error {
		got, err := tx.FileSizes()
		if err == nil && !maps.Equal(got, map[string]int64{"record.json": 16}) {
			t.Fatalf("stale peer accounting: %v", got)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"record.json", "storage-revision", "store.lock"} {
		info, err := os.Stat(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Errorf("%s permissions = %v", name, info.Mode().Perm())
		}
	}
}

func TestStoreFailedPublicationInvalidatesFileSizes(t *testing.T) {
	directory := t.TempDir()
	store := fixtureStore(directory, managedRecord)
	failure := errors.New("publication interrupted after removal")
	err := store.Transaction(t.Context(), nil, func(tx *history.Transaction) error {
		if err := tx.Write("record.json", "record-pending-", []byte("old")); err != nil {
			return err
		}
		if _, err := tx.FileSizes(); err != nil {
			return err
		}
		// A removal participant invalidates the shared view before effects. A
		// later publication failure must not resurrect its pre-removal sizes.
		if err := tx.AdvanceRevision(); err != nil {
			return err
		}
		if err := os.Remove(filepath.Join(directory, "record.json")); err != nil {
			return err
		}
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatalf("publication error = %v", err)
	}
	if err := store.Transaction(t.Context(), nil, func(tx *history.Transaction) error {
		sizes, err := tx.FileSizes()
		if err == nil && len(sizes) != 0 {
			t.Fatalf("failed publication retained removed bytes: %v", sizes)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestStoreEscapedTransactionRejectsEffects(t *testing.T) {
	directory := t.TempDir()
	store := fixtureStore(directory, managedRecord)
	var escaped *history.Transaction
	if err := store.Transaction(t.Context(), nil, func(tx *history.Transaction) error {
		escaped = tx
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := escaped.Write("record.json", "record-pending-", []byte("escaped")); err == nil {
		t.Error("escaped transaction published a record")
	}
	if err := escaped.AdvanceRevision(); err == nil {
		t.Error("escaped transaction advanced revision")
	}
	if _, err := escaped.Revision(); err == nil {
		t.Error("escaped transaction read revision")
	}
	if _, err := escaped.FileSizes(); err == nil {
		t.Error("escaped transaction read accounting")
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 || entries[0].Name() != "store.lock" {
		t.Fatalf("escaped effects: entries=%v err=%v", entries, err)
	}
}

func TestStoreCrossProcessSerializationAndCancellation(t *testing.T) {
	directory := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStoreLockProcess$")
	cmd.Env = append(os.Environ(), "MEKUGI_HISTORY_LOCK_TEST="+directory)
	cmd.Stderr = os.Stderr
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		cancel()
		if !waited {
			_ = cmd.Wait()
		}
	})
	if line, err := bufio.NewReader(output).ReadString('\n'); err != nil || line != "locked\n" {
		t.Fatalf("child readiness = %q, %v", line, err)
	}
	store := fixtureStore(directory, managedRecord)
	wait, stop := context.WithTimeout(t.Context(), 75*time.Millisecond)
	err = store.Transaction(wait, nil, func(*history.Transaction) error {
		t.Error("entered transaction while another process held the lock")
		return nil
	})
	stop()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contended transaction error = %v", err)
	}
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	waited = true
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Transaction(ctx, nil, func(tx *history.Transaction) error {
		sizes, err := tx.FileSizes()
		if err == nil && !maps.Equal(sizes, map[string]int64{"peer.json": 4}) {
			t.Fatalf("child publication unavailable after unlock: %v", sizes)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestStoreLockProcess(t *testing.T) {
	directory := os.Getenv("MEKUGI_HISTORY_LOCK_TEST")
	if directory == "" {
		return
	}
	store := fixtureStore(directory, managedRecord)
	if err := store.Transaction(t.Context(), nil, func(tx *history.Transaction) error {
		if err := tx.Write("peer.json", "record-pending-", []byte("peer")); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(os.Stdout, "locked"); err != nil {
			return err
		}
		_, err := io.Copy(io.Discard, os.Stdin)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func fixtureStore(directory string, managed func(string) bool) *history.Store {
	service, err := history.NewService([]recordkind.Spec{{ID: "fixture", Match: managed}})
	if err != nil {
		panic(err)
	}
	return service.Attach(directory)
}
