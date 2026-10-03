package router

import (
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

type runtimeMChangesResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exitCode"`
}

func runtimeMChangesInput(t *testing.T, args ...string) string {
	t.Helper()
	if args == nil {
		args = []string{}
	}
	data, err := json.Marshal(&struct {
		Args []string `json:"args"`
	}{args})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func runtimeMChangesDecode(t *testing.T, body string) runtimeMChangesResult {
	t.Helper()
	var result runtimeMChangesResult
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || result.Stderr != "" {
		t.Fatalf("change read failed: %+v", result)
	}
	return result
}

func runtimeMChangesEdit(t *testing.T, s *ObservationService, b ObservationBinding, name string) string {
	t.Helper()
	path := filepath.Join(b.Workspace, name+".txt")
	nativeObservationWrite(t, path, "before\n")
	call := ObservationCall{Binding: b, ID: name, Tool: "Edit", Input: name, Paths: []string{path}}
	if err := s.owner.before(t.Context(), call); err != nil {
		t.Fatal(err)
	}
	nativeObservationWrite(t, path, "after\n")
	id, err := s.owner.after(t.Context(), call, ObservationTerminal{Status: "completed"})
	if err != nil || id == "" {
		t.Fatalf("observed edit: %q %v", id, err)
	}
	return id
}

func TestRuntimeMChangesReceiptOwnerIsolation(t *testing.T) {
	s, root, c := runtimeJournalFixture(t)
	child := root
	child.Agent = "child"
	runtimeJournalBind(t, s, child, c)
	rootID := runtimeMChangesEdit(t, s, root, "root-edit")
	childID := runtimeMChangesEdit(t, s, child, "child-edit")
	for i, args := range [][]string{{}, {"--mine"}, {"--list"}} {
		input := runtimeMChangesInput(t, args...)
		rootCall, childCall := fmt.Sprintf("root-read-%d", i), fmt.Sprintf("child-read-%d", i)
		// The later child hook must not replace the root call's authenticated scope.
		runtimeJournalReceipt(t, s, root, c, rootCall, "mchanges", input)
		runtimeJournalReceipt(t, s, child, c, childCall, "mchanges", input)
		for _, check := range []struct{ call, want, absent string }{{rootCall, "root-edit", "child-edit"}, {childCall, "child-edit", "root-edit"}} {
			result := runtimeMChangesDecode(t, runtimeJournalInvoke(t, s, c, check.call, "mchanges", input))
			if i == 2 {
				wantID := rootID
				if check.call == childCall {
					wantID = childID
				}
				if result.Stdout != wantID+" +1 -1\n" {
					t.Fatalf("borrowed list caller for %s: %q", check.call, result.Stdout)
				}
				continue
			}
			if !strings.Contains(result.Stdout, check.want) || strings.Contains(result.Stdout, check.absent) {
				t.Fatalf("borrowed caller for %s: %q", check.call, result.Stdout)
			}
		}
	}
	for i, check := range []struct {
		binding  ObservationBinding
		id, name string
	}{{root, childID, "child-edit.txt"}, {child, rootID, "root-edit.txt"}} {
		input := runtimeMChangesInput(t, "--summary", check.id)
		call := fmt.Sprintf("explicit-%d", i)
		runtimeJournalReceipt(t, s, check.binding, c, call, "mchanges", input)
		result := runtimeMChangesDecode(t, runtimeJournalInvoke(t, s, c, call, "mchanges", input))
		if result.Stdout != "M\t1\t1\t"+check.name+"\n" {
			t.Fatalf("explicit ID read: %q", result.Stdout)
		}
	}
}

func TestRuntimeMChangesRejectsUntrustedAndMutatingRequests(t *testing.T) {
	s, b, c := runtimeJournalFixture(t)
	id := runtimeMChangesEdit(t, s, b, "retained")
	input := runtimeMChangesInput(t, "--summary", id)
	reject := func(call, input string) {
		t.Helper()
		if status, body := observationPost(t, s, c, s.Endpoint().Token, observationRequest{Operation: "mchanges", NativeID: call, Input: input}); status != http.StatusUnprocessableEntity {
			t.Fatalf("unsafe request accepted: %d %s", status, body)
		}
		if got := readTestFile(t, filepath.Join(b.Workspace, "retained.txt")); got != "after\n" {
			t.Fatalf("rejected request changed filesystem: %q", got)
		}
	}
	reject("missing", input)
	runtimeJournalReceipt(t, s, b, c, "wrong-tool", "journal_read", input)
	reject("wrong-tool", input)
	runtimeJournalReceipt(t, s, b, c, "changed-args", "mchanges", input)
	reject("changed-args", runtimeMChangesInput(t, "--list"))
	reject("changed-args", `{"args":`)
	for i, args := range [][]string{{"apply", id}, {"revert", id}, {"--summary", id, "--workspace", t.TempDir()}} {
		call := fmt.Sprintf("rejected-%d", i)
		input := runtimeMChangesInput(t, args...)
		runtimeJournalReceipt(t, s, b, c, call, "mchanges", input)
		reject(call, input)
	}
}

func TestRuntimeMChangesReopenedStoreKeepsImplicitOwner(t *testing.T) {
	s, root, c := runtimeJournalFixture(t)
	child := root
	child.Agent = "child"
	runtimeJournalBind(t, s, child, c)
	runtimeMChangesEdit(t, s, root, "root-edit")
	runtimeMChangesEdit(t, s, child, "child-edit")
	input := runtimeMChangesInput(t, "--mine", "--summary")
	runtimeJournalReceipt(t, s, child, c, "saved-read", "mchanges", input)
	s.owner.close()
	store, err := openMekugiReplayStore(s.owner.store.directory)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := newNativeObservationOwner(t.Context(), store, root.Runtime, root.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.close()
	restored := &ObservationService{owner: owner}
	restored.EnableJournal()
	for _, b := range []ObservationBinding{root, child} {
		if err := owner.bind(t.Context(), b); err != nil {
			t.Fatal(err)
		}
		if err := restored.journal.bind(t.Context(), b); err != nil {
			t.Fatal(err)
		}
	}
	body, err := restored.journal.invoke(t.Context(), "mchanges", "saved-read", input)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	result := runtimeMChangesDecode(t, string(data))
	if result.Stdout != "M\t1\t1\tchild-edit.txt\n" {
		t.Fatalf("reopened caller lost: %q", result.Stdout)
	}
}
