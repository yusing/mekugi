package control

import (
	"context"
	json "encoding/json/v2"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

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
