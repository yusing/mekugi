package executionobservation

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func TestRecordTransactionsEvidenceFailureKeepsMembershipUnpublished(t *testing.T) {
	t.Parallel()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, _ := retentionTestSession(t, store, "writer", 0)
	selected := store.scoped(ctx).changeScope()
	id, err := store.changes.Reserve(ctx, selected, "/w", "writer", "capture")
	if err != nil {
		t.Fatal(err)
	}
	retentionTestPut(t, store, ctx, "/w", "conflict")
	var delivered []changes.Update
	store.liveDiff = func(updates []changes.Update) { delivered = append(delivered, updates...) }
	evidence := snapshotTestHistory()
	evidence.ChangeID, evidence.CorrelationID, evidence.sequence = id, "capture", 1
	batch := map[string]mekugiHistory{
		"evidence": evidence,
		"conflict": {Script: "different retained input", sequence: 2},
	}
	if err := store.put(ctx, "/w", batch); err == nil {
		t.Fatal("conflicting host facts were published")
	}
	retained, found, err := store.read("/w", "evidence", false)
	if err != nil || !found || retained.CaptureOrder == 0 {
		t.Fatalf("completed evidence was lost: %+v %v %v", retained, found, err)
	}
	index, err := store.changes.Inspect(selected, "/w")
	if err != nil || len(index.Changes[id].Calls) != 0 || len(delivered) != 0 {
		t.Fatalf("failed batch exposed membership: %+v %+v %v", index, delivered, err)
	}
	if err := store.put(ctx, "/w", map[string]mekugiHistory{"evidence": evidence}); err != nil {
		t.Fatal(err)
	}
	retried, found, err := store.read("/w", "evidence", false)
	if err != nil || !found || retried.CaptureOrder != retained.CaptureOrder || len(delivered) != 1 {
		t.Fatalf("retry changed capture identity or delivery: %+v %+v %v", retried, delivered, err)
	}
	text, err := store.changes.Read(ctx, selected, changes.Query{Workspace: "/w", IDs: []string{id}, View: "summary", MaxTokens: 8000})
	if err != nil || !strings.Contains(text, "missing before") {
		t.Fatalf("published partial capture lost its evidence: %q %v", text, err)
	}
}

type interruptedForkChanges struct {
	changes.Service
	check func() error
}

func (s interruptedForkChanges) PrepareFork(selected changes.Scope, source, dest, fork string) (changes.ForkPlan, error) {
	plan, err := s.Service.PrepareFork(selected, source, dest, fork)
	return interruptedForkPlan{ForkPlan: plan, check: s.check}, err
}

type interruptedForkPlan struct {
	changes.ForkPlan
	check func() error
}

func (p interruptedForkPlan) Publish(tx *history.Transaction) error {
	if err := p.ForkPlan.Publish(tx); err != nil {
		return err
	}
	return p.check()
}

func TestRecordTransactionsInterruptedForkRetriesBeforeScopePublication(t *testing.T) {
	t.Parallel()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Other retained namespaces can belong to features absent from this build.
	_, _ = retentionTestSession(t, store, "legacy-coordinator", 0)
	legacy := `{"Version":1,"Thread":"legacy-coordinator","Namespace":"legacy-coordinator","Coordinator":{"main":"legacy-coordinator","directory":"/legacy","workspace":"/other"},"Next":0}`
	legacyPath := filepath.Join(store.directory, history.ScopeName("legacy-coordinator"))
	if err := os.WriteFile(legacyPath, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	parent, releaseParent := retentionTestSession(t, store, "parent", 0)
	parent = bindTestHandleScope(t, store, parent, "", "")
	id, err := store.changes.Reserve(parent, store.scoped(parent).changeScope(), "/w", "parent", "capture")
	if err != nil {
		t.Fatal(err)
	}
	evidence := snapshotTestHistory()
	evidence.ChangeID, evidence.CorrelationID = id, "capture"
	if err := store.put(parent, "/w", map[string]mekugiHistory{"evidence": evidence}); err != nil {
		t.Fatal(err)
	}
	ref, err := store.publishOutput(parent, command.OmittedOutput{Stdout: "inherited output"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	fork, _ := retentionTestSession(t, store, "fork", 0)
	fork, err = store.prepareHandleScope(fork, codexresponses.TurnMetadata{ThreadID: "fork", ForkedFromThreadID: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	fault := errors.New("interrupted after cloned index")
	owner := store.changes
	store.changes = interruptedForkChanges{Service: owner, check: func() error {
		catalog, err := store.publication.ReadCatalog(history.SessionName("fork"))
		if err != nil || !catalog.Files[legacycall.Name("/w", "evidence", false)] {
			t.Fatalf("cloned index preceded its dependencies: %+v %v", catalog, err)
		}
		if _, exists, err := store.publication.ReadScope("fork"); err != nil || exists {
			t.Fatalf("fork scope preceded inherited publication: %v %v", exists, err)
		}
		return fault
	}}
	if err := store.retainInput(fork, "/w", nil, nil, nil); !errors.Is(err, fault) {
		t.Fatalf("interrupted initialization: %v", err)
	}
	store.changes = owner
	if _, exists, err := store.publication.ReadScope("fork"); err != nil || exists {
		t.Fatalf("interrupted fork became initialized: %v %v", exists, err)
	}
	if err := store.retainInput(fork, "/w", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(legacyPath); err != nil || string(data) != legacy {
		t.Fatal("fork rewrote unrelated coordinator authority")
	}
	releaseParent()
	retentionTestAge(t, store, "parent", 30*24*time.Hour)
	if err := store.cleanupSessions(fork); err != nil {
		t.Fatal(err)
	}
	reopened, err := openMekugiReplayStore(store.directory)
	if err != nil {
		t.Fatal(err)
	}
	output, err := reopened.recoverOutput(fork, ref)
	if err != nil || output.Stdout != "inherited output" {
		t.Fatalf("fork lost inherited recovery: %+v %v", output, err)
	}
	text, err := reopened.changes.Read(fork, reopened.scoped(fork).changeScope(), changes.Query{Workspace: "/w", IDs: []string{id}, View: "summary", MaxTokens: 8000})
	if err != nil || !strings.Contains(text, "missing before") {
		t.Fatalf("fork lost inherited partial capture: %q %v", text, err)
	}
}
