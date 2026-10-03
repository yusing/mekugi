package router

import (
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func runtimeJournalFixture(t *testing.T) (*ObservationService, ObservationBinding, *http.Client) {
	t.Helper()
	s, b, c := observationHTTPFixture(t)
	s.EnableJournal()
	runtimeJournalBind(t, s, b, c)
	return s, b, c
}
func runtimeJournalBind(t *testing.T, s *ObservationService, b ObservationBinding, c *http.Client) {
	t.Helper()
	if status, body := observationPost(t, s, c, s.Endpoint().Token, observationRequest{Operation: "bind", Binding: b}); status != http.StatusOK {
		t.Fatalf("bind: %d %s", status, body)
	}
}
func runtimeJournalReceipt(t *testing.T, s *ObservationService, b ObservationBinding, c *http.Client, id, op, input string) {
	t.Helper()
	call := ObservationCall{Binding: b, ID: id, Tool: "mcp__mekugi__" + op, Input: input}
	if status, body := observationPost(t, s, c, s.Endpoint().Token, observationRequest{Operation: "journal_before", Call: call}); status != http.StatusOK {
		t.Fatalf("receipt: %d %s", status, body)
	}
}
func runtimeJournalInvoke(t *testing.T, s *ObservationService, c *http.Client, id, op, input string) string {
	t.Helper()
	status, body := observationPost(t, s, c, s.Endpoint().Token, observationRequest{Operation: op, NativeID: id, Input: input})
	if status != http.StatusOK {
		t.Fatalf("invoke %s: %d %s", id, status, body)
	}
	return body
}
func runtimeJournalAdd(t *testing.T, s *ObservationService, b ObservationBinding, c *http.Client, id, title string) string {
	t.Helper()
	input := fmt.Sprintf(`{"journal":[{"op":"add","kind":"task","title":%q,"state":"working"}]}`, title)
	runtimeJournalReceipt(t, s, b, c, id, "journal_batch", input)
	return runtimeJournalInvoke(t, s, c, id, "journal_batch", input)
}
func runtimeJournalRead(t *testing.T, s *ObservationService, b ObservationBinding, c *http.Client, id, input string) []journalNode {
	t.Helper()
	runtimeJournalReceipt(t, s, b, c, id, "journal_read", input)
	body := runtimeJournalInvoke(t, s, c, id, "journal_read", input)
	var out struct {
		Nodes []journalNode `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	return out.Nodes
}
func TestRuntimeJournalReceiptAuthority(t *testing.T) {
	s, b, c := runtimeJournalFixture(t)
	input := `{"journal":[{"op":"add","kind":"task","title":"Trusted","state":"working"}]}`
	reject := func(id, op, args string) {
		t.Helper()
		if status, body := observationPost(t, s, c, s.Endpoint().Token, observationRequest{Operation: op, NativeID: id, Input: args}); status != http.StatusUnprocessableEntity {
			t.Fatalf("unauthorized accepted: %d %s", status, body)
		}
	}
	reject("missing", "journal_batch", input)
	runtimeJournalReceipt(t, s, b, c, "trusted", "journal_batch", input)
	reject("trusted", "journal_read", input)
	reject("trusted", "journal_batch", `{"journal":[]}`)
	reject("trusted", "journal_batch", `{"journal":`)
	wrong := b
	wrong.Session = "another"
	if status, _ := observationPost(t, s, c, s.Endpoint().Token, observationRequest{Operation: "journal_before", Call: ObservationCall{Binding: wrong, ID: "wrong", Tool: "mcp__mekugi__journal_batch", Input: input}}); status != http.StatusUnprocessableEntity {
		t.Fatal("unbound session accepted")
	}
	for _, tool := range []string{"Write", "journal_batch"} {
		if status, _ := observationPost(t, s, c, s.Endpoint().Token, observationRequest{Operation: "journal_before", Call: ObservationCall{Binding: b, ID: tool, Tool: tool, Input: input}}); status != http.StatusUnprocessableEntity {
			t.Fatal("wrong native tool accepted")
		}
	}
	runtimeJournalInvoke(t, s, c, "trusted", "journal_batch", ` { "journal" : [ {"state":"working","title":"Trusted","kind":"task","op":"add"} ] } `)
	changed := ObservationCall{Binding: b, ID: "trusted", Tool: "mcp__mekugi__journal_batch", Input: `{"journal":[]}`}
	if status, _ := observationPost(t, s, c, s.Endpoint().Token, observationRequest{Operation: "journal_before", Call: changed}); status != http.StatusUnprocessableEntity {
		t.Fatal("receipt replacement accepted")
	}
}
func TestRuntimeJournalAtomicRollbackAndDuplicateRetry(t *testing.T) {
	s, b, c := runtimeJournalFixture(t)
	input := `{"journal":[{"op":"add","kind":"task","title":"Rolled back"},{"op":"set","p":"/99","state":"done"}]}`
	runtimeJournalReceipt(t, s, b, c, "invalid", "journal_batch", input)
	for range 2 {
		if status, _ := observationPost(t, s, c, s.Endpoint().Token, observationRequest{Operation: "journal_batch", NativeID: "invalid", Input: input}); status != http.StatusUnprocessableEntity {
			t.Fatal("invalid transaction accepted")
		}
	}
	if nodes := runtimeJournalRead(t, s, b, c, "empty", `{}`); len(nodes) != 0 {
		t.Fatalf("partial commit: %+v", nodes)
	}
	first := runtimeJournalAdd(t, s, b, c, "valid", "Committed")
	retry := runtimeJournalAdd(t, s, b, c, "valid", "Committed")
	if first != retry {
		t.Fatalf("unstable receipt paths: %s != %s", first, retry)
	}
	nodes := runtimeJournalRead(t, s, b, c, "nodes", `{}`)
	if len(nodes) != 1 || nodes[0].Path != "/1" || nodes[0].Title != "Committed" {
		t.Fatalf("rollback/dedup: %+v", nodes)
	}
	if len(s.journal.sink().snapshot()) != 1 {
		t.Fatal("duplicate publication")
	}
}
func TestRuntimeJournalConcurrentRootChildIsolation(t *testing.T) {
	s, b, c := runtimeJournalFixture(t)
	child := b
	child.Agent = "child"
	runtimeJournalBind(t, s, child, c)
	var wg sync.WaitGroup
	for i, binding := range []ObservationBinding{b, child} {
		wg.Go(func() { runtimeJournalAdd(t, s, binding, c, fmt.Sprintf("add-%d", i), fmt.Sprintf("Owner %d", i)) })
	}
	wg.Wait()
	for i, binding := range []ObservationBinding{b, child} {
		nodes := runtimeJournalRead(t, s, binding, c, fmt.Sprintf("read-%d", i), `{}`)
		if len(nodes) != 1 || nodes[0].Title != fmt.Sprintf("Owner %d", i) || nodes[0].Path != "/1" {
			t.Fatalf("borrowed/mounted identity: %+v", nodes)
		}
	}
	input := `{"agent":"/root"}`
	runtimeJournalReceipt(t, s, child, c, "ancestor", "journal_read", input)
	if status, _ := observationPost(t, s, c, s.Endpoint().Token, observationRequest{Operation: "journal_read", NativeID: "ancestor", Input: input}); status != http.StatusUnprocessableEntity {
		t.Fatal("unproven root read accepted")
	}
	input = `{"journal":[{"op":"add","kind":"task","title":"Mount","agent":"/root/child"}]}`
	runtimeJournalReceipt(t, s, b, c, "mount", "journal_batch", input)
	if status, _ := observationPost(t, s, c, s.Endpoint().Token, observationRequest{Operation: "journal_batch", NativeID: "mount", Input: input}); status != http.StatusUnprocessableEntity {
		t.Fatal("fabricated child mount accepted")
	}
}
func TestRuntimeJournalPersistencePublicationAndRestart(t *testing.T) {
	s, b, c := runtimeJournalFixture(t)
	runtimeJournalAdd(t, s, b, c, "saved", "Persisted")
	disk, exists, err := readThreadJournal(s.owner.store, b.Workspace, observationThread(b))
	if err != nil || !exists || len(disk.Items) != 1 || len(s.journal.sink().snapshot()) != 1 {
		t.Fatalf("publication without persistence: %+v %v", disk, err)
	}
	// A restart releases the old launch ownership before claiming the same native session.
	s.owner.close()
	store, err := openMekugiReplayStore(s.owner.store.directory)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := newNativeObservationOwner(t.Context(), store, b.Runtime, b.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.close()
	restored := &ObservationService{owner: owner}
	restored.EnableJournal()
	if err := owner.bind(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	if err := restored.journal.bind(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	pending := restored.journal.sink().snapshot()
	if len(pending) != 1 || pending[0].event.Fields.Title != "Persisted" {
		t.Fatalf("restart lost unpainted revision: %+v", pending)
	}
	ctx, err := restored.journal.scope(t.Context(), b)
	if err != nil {
		t.Fatal(err)
	}
	replay, _, err := readThreadJournal(store.scoped(ctx), b.Workspace, observationThread(b))
	if err != nil || !reflect.DeepEqual(disk.Items, replay.Items) {
		t.Fatalf("restart changed nodes: %v", err)
	}
}
func TestRuntimeJournalStorageFailureRetainsRevision(t *testing.T) {
	s, b, c := runtimeJournalFixture(t)
	runtimeJournalAdd(t, s, b, c, "initial", "Initial")
	sink := s.journal.sink()
	before := sink.snapshot()
	input := `{"journal":[{"op":"add","kind":"note","title":"Must not publish"}]}`
	runtimeJournalReceipt(t, s, b, c, "failed", "journal_batch", input)
	path := filepath.Join(s.owner.store.directory, journalFilename(b.Workspace, observationThread(b)))
	saved := path + ".saved"
	if err := os.Rename(path, saved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	restored := false
	t.Cleanup(func() {
		if restored {
			return
		}
		if err := os.Remove(path); err != nil {
			t.Error(err)
		}
		if err := os.Rename(saved, path); err != nil {
			t.Error(err)
		}
	})
	if status, _ := observationPost(t, s, c, s.Endpoint().Token, observationRequest{Operation: "journal_batch", NativeID: "failed", Input: input}); status != http.StatusUnprocessableEntity {
		t.Fatal("storage failure accepted")
	}
	if after := sink.snapshot(); !reflect.DeepEqual(before, after) {
		t.Fatalf("failed storage published: %+v", after)
	}
	ctx, err := s.journal.scope(t.Context(), b)
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.acknowledgeOwned(ctx, s.journal.journals, s.owner.store, before); err == nil {
		t.Fatal("failed receipt storage accepted")
	}
	if !reflect.DeepEqual(before, sink.snapshot()) {
		t.Fatal("failed acknowledgement discarded revision")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(saved, path); err != nil {
		t.Fatal(err)
	}
	restored = true
	runtimeJournalInvoke(t, s, c, "failed", "journal_batch", input)
	nodes := runtimeJournalRead(t, s, b, c, "recovered", `{}`)
	if len(nodes) != 2 || nodes[1].Path != "/2" {
		t.Fatalf("failed storage consumed path or retry: %+v", nodes)
	}
}

func TestRuntimeJournalRejectsUnsupportedBatchAndReadScope(t *testing.T) {
	s, b, c := runtimeJournalFixture(t)
	for i, input := range []string{
		`{"journal":[{"op":"finish"}]}`,
		`{"journal":[{"op":"add","text":"Legacy"}]}`,
		`{"journal":[{"op":"add","kind":"task","title":"Scope injection"}],"thread":"root"}`,
		`{"journal":[]}`,
	} {
		id := fmt.Sprintf("unsupported-%d", i)
		runtimeJournalReceipt(t, s, b, c, id, "journal_batch", input)
		if status, _ := observationPost(t, s, c, s.Endpoint().Token, observationRequest{Operation: "journal_batch", NativeID: id, Input: input}); status != http.StatusUnprocessableEntity {
			t.Fatalf("unsupported input accepted: %s", input)
		}
	}
	input := `{"thread":"another"}`
	runtimeJournalReceipt(t, s, b, c, "read-scope", "journal_read", input)
	if status, _ := observationPost(t, s, c, s.Endpoint().Token, observationRequest{Operation: "journal_read", NativeID: "read-scope", Input: input}); status != http.StatusUnprocessableEntity {
		t.Fatal("model-supplied read identity accepted")
	}
	if nodes := runtimeJournalRead(t, s, b, c, "unchanged", `{}`); len(nodes) != 0 {
		t.Fatalf("rejected input mutated tree: %+v", nodes)
	}
}
