package control

import (
	"context"
	json "encoding/json/v2"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func TestCompletionSelectionUsesSharedReferences(t *testing.T) {
	for _, tc := range []struct{ message, selection string }{
		{"Finished. Captures: mchanges:amber1..3,birch2. Again `mchanges:amber1..3,birch2`.", `{"sources":["mchanges:amber1..3,birch2"]}`},
		{"See @source.go, $skill and mchanges:amber1, then mchanges:birch2..4!", `{"sources":["mchanges:amber1","mchanges:birch2..4"]}`},
		{"Malformed mchanges:amber1.. retains a diagnostic.", `{"sources":["mchanges:amber1.."]}`},
		{`{"sources":["mchanges:amber1"]}`, `{"sources":["mchanges:amber1"]}`},
		{`Escaped \mchanges:amber1; @source.go and $skill are not completion sources.`, ""},
		{`{"sources":[{"changes":["amber1"]}]}`, ""},
	} {
		if got := CompletionSelection(tc.message); got != tc.selection {
			t.Errorf("selection from %q = %s, want %s", tc.message, got, tc.selection)
		}
	}
}

func TestMessageSnapshotConcurrentAcquisitionAndRestore(t *testing.T) {
	s, session := fixture(t)
	caller := identity("root", "rt")
	caller.Call = "message-capture"
	var reads atomic.Int32
	var workers sync.WaitGroup
	refs := make(chan string, 8)
	for range 8 {
		workers.Go(func() {
			ref, err := s.FreezeMessage(t.Context(), caller, "message:child", "Read @source", func() (string, error) {
				reads.Add(1)
				return "FROZEN_BODY", nil
			})
			if err != nil {
				t.Error(err)
			}
			refs <- ref
		})
	}
	workers.Wait()
	close(refs)
	reference := <-refs
	for ref := range refs {
		if ref != reference {
			t.Fatal("concurrent operation retained different snapshots")
		}
	}
	if reads.Load() != 1 {
		t.Fatal("concurrent operation repeated acquisition", reads.Load())
	}
	for _, changed := range []struct{ route, message string }{{"other", "Read @source"}, {"message:child", "Read @different"}} {
		if _, err := s.FreezeMessage(t.Context(), caller, changed.route, changed.message, func() (string, error) { panic("conflicting retry acquired") }); err == nil {
			t.Fatal("conflicting snapshot accepted")
		}
	}
	_, err := s.PrepareActionWithSources(t.Context(), caller, "root", "message", "Read @source", reference)
	must(t, err)
	caller.Call = "spawn-with-import"
	imported, err := s.PublishImport(t.Context(), caller, []byte("retained native fork fixture"))
	must(t, err)
	_, err = s.ReserveSpawn(t.Context(), caller, SpawnRequest{Name: "history-child", Import: imported, Message: "Work.", Settings: Settings{Workspace: t.TempDir()}})
	must(t, err)
	dependencies, err := session.Store.Dependencies(recordName(session.Namespace))
	must(t, err)
	if !slices.Equal(dependencies, []string{reference, imported}) {
		t.Fatalf("checkpoint lost source or child-import dependency: %v", dependencies)
	}
	tree, err := s.Snapshot("root")
	must(t, err)
	raw, err := json.Marshal(tree)
	must(t, err)
	if strings.Contains(string(raw), "FROZEN_BODY") {
		t.Fatal("source bodies accumulated in the coordination tree")
	}
	restored := New()
	must(t, restored.Restore(t.Context(), session))
	text, err := restored.MessageText("root", reference, "Read @source")
	must(t, err)
	if !strings.Contains(text, "FROZEN_BODY") {
		t.Fatal("cold restore lost source snapshot")
	}
	importPath, err := restored.ImportPath("root", imported)
	must(t, err)
	data, err := history.ReadRecord(importPath)
	must(t, err)
	if string(data) != "retained native fork fixture" {
		t.Fatalf("cold restore lost child import: %q", data)
	}
}

func TestMessageCaptureDoesNotHoldCoordinationLock(t *testing.T) {
	s, _ := fixture(t)
	caller := identity("root", "rt")
	caller.Call = "message-capture"
	started, release := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, err := s.FreezeMessage(t.Context(), caller, "message:root", "@source", func() (string, error) {
			close(started)
			<-release
			return "body", nil
		})
		result <- err
	}()
	<-started
	other := caller
	other.Call = "unrelated"
	_, err := s.PrepareAction(t.Context(), other, "root", "message", "unrelated")
	close(release)
	must(t, err)
	must(t, <-result)
	_, err = s.FreezeMessage(context.Background(), Identity{Thread: "root", Session: "foreign", Turn: "rt", Call: "foreign"}, "message:root", "@source", func() (string, error) { panic("unauthenticated acquisition") })
	if err == nil {
		t.Fatal("foreign caller acquired a snapshot")
	}
}
