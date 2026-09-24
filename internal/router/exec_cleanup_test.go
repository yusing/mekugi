package router

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestMergeExecObservationsDoesNotMutateFirstMember(t *testing.T) {
	commands := []execCommandInput{{Command: "first"}, {Command: "command spare"}}
	labels := []string{"shared", "label spare"}
	programs := []execProgram{{Label: "shared"}, {Label: "program spare"}}
	roots := []string{"/workspace/project", "/root spare"}
	files := []execFileSnapshot{{Path: "/workspace/first"}, {Path: "/file spare"}}
	omitted := []execOmission{{Path: "/workspace/omitted"}, {Path: "/omission spare"}}
	listings := []execListing{{Root: "/workspace/listed"}, {Root: "/listing spare"}}
	excluded := []string{"/workspace/excluded", "/excluded spare"}
	first := &execObservation{
		Commands: commands[:1], Class: execScoped.String(), Reason: "first reason", Labels: labels[:1],
		Files: files[:1], Omitted: omitted[:1], Listings: listings[:1], Roots: roots[:1],
		Programs: programs[:1], Excluded: excluded[:1], WindowStart: time.Unix(30, 0),
	}
	second := &execObservation{
		Commands: []execCommandInput{{Command: "second"}}, Class: execOpaque.String(), Reason: "stronger reason",
		Labels: []string{"shared", "second"}, Programs: []execProgram{{Label: "shared"}, {Label: "second"}},
		Roots: []string{"/workspace", "/outside"}, Files: []execFileSnapshot{{Path: "/workspace/second"}},
		Omitted: []execOmission{{Path: "/workspace/omitted-2"}}, Listings: []execListing{{Root: "/workspace/listed-2"}},
		Excluded: []string{"/workspace/excluded-2"}, WindowStart: time.Unix(20, 0), CodeMode: true,
	}
	third := &execObservation{
		Commands: []execCommandInput{{Command: "third"}}, Labels: []string{"third"},
		Programs: []execProgram{{Label: "third"}}, Roots: []string{"/workspace/child", "/last"},
		Files: []execFileSnapshot{{Path: "/workspace/third"}}, WindowStart: time.Unix(10, 0),
	}
	firstBefore := *first
	firstBefore.Commands = append([]execCommandInput(nil), first.Commands...)
	firstBefore.Labels = append([]string(nil), first.Labels...)
	firstBefore.Programs = append([]execProgram(nil), first.Programs...)
	firstBefore.Roots = append([]string(nil), first.Roots...)
	firstBefore.Files = append([]execFileSnapshot(nil), first.Files...)
	firstBefore.Omitted = append([]execOmission(nil), first.Omitted...)
	firstBefore.Listings = append([]execListing(nil), first.Listings...)
	firstBefore.Excluded = append([]string(nil), first.Excluded...)

	merged := mergeExecObservations([]execCompletion{
		{history: mekugiHistory{ExecObservation: first}},
		{history: mekugiHistory{ExecObservation: second}},
		{history: mekugiHistory{ExecObservation: third}},
	})
	if !reflect.DeepEqual(*first, firstBefore) {
		t.Fatalf("merge mutated first member: got %+v, want %+v", *first, firstBefore)
	}
	if commands[1].Command != "command spare" || labels[1] != "label spare" || programs[1].Label != "program spare" ||
		roots[1] != "/root spare" || files[1].Path != "/file spare" || omitted[1].Path != "/omission spare" ||
		listings[1].Root != "/listing spare" || excluded[1] != "/excluded spare" {
		t.Fatalf("merge overwrote spare-capacity storage in first member: commands=%+v labels=%+v programs=%+v roots=%+v files=%+v omitted=%+v listings=%+v excluded=%+v",
			commands, labels, programs, roots, files, omitted, listings, excluded)
	}
	if got, want := merged.Commands, []execCommandInput{{Command: "first"}, {Command: "second"}, {Command: "third"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("commands = %+v, want %+v", got, want)
	}
	if got, want := merged.Labels, []string{"shared", "second", "third"}; !reflect.DeepEqual(got, want) {
		t.Errorf("labels = %v, want %v", got, want)
	}
	if got, want := merged.Programs, []execProgram{{Label: "shared"}, {Label: "second"}, {Label: "third"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("programs = %+v, want %+v", got, want)
	}
	if got, want := merged.Roots, []string{"/workspace", "/outside", "/last"}; !reflect.DeepEqual(got, want) {
		t.Errorf("roots = %v, want %v", got, want)
	}
	if got, want := merged.Files, []execFileSnapshot{{Path: "/workspace/first"}, {Path: "/workspace/second"}, {Path: "/workspace/third"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("files = %+v, want %+v", got, want)
	}
	if got, want := merged.Omitted, []execOmission{{Path: "/workspace/omitted"}, {Path: "/workspace/omitted-2"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("omitted = %+v, want %+v", got, want)
	}
	if got, want := merged.Listings, []execListing{{Root: "/workspace/listed"}, {Root: "/workspace/listed-2"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("listings = %+v, want %+v", got, want)
	}
	if got, want := merged.Excluded, []string{"/workspace/excluded", "/workspace/excluded-2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("excluded = %v, want %v", got, want)
	}
	if merged.Class != execOpaque.String() || merged.Reason != "stronger reason" || !merged.CodeMode || !merged.WindowStart.Equal(time.Unix(10, 0)) {
		t.Errorf("scalar merge fields = %+v", merged)
	}
}

func TestExecScopePreviewPublishesOnlyReviewChanges(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "tracked.txt")
		if err := os.WriteFile(path, []byte("original\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		before := snapshotExecFile(path, nil)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		broker := newLiveDiffBroker(ctx)
		broker.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{root: {"thread": true}}})
		sub := broker.subscribe()
		broker.takePreviews(sub) // Discard initial scope synchronization.
		const previewID = "running:cleanup-test"
		go runExecScopePreview(ctx, broker, execObservation{Class: execScoped.String(), Files: []execFileSnapshot{before}}, liveDiffPreview{
			ID: previewID, Workspace: root, Thread: "thread",
		})
		synctest.Wait()
		assertNoExecCleanupPreview(t, broker.takePreviews(sub), "unchanged scope should stay out of the pane")

		advanceExecCleanupPreviewTicker(t)
		assertNoExecCleanupPreview(t, broker.takePreviews(sub), "unchanged scope should stay out of the pane after polling")
		advanceExecCleanupPreviewTicker(t)
		assertNoExecCleanupPreview(t, broker.takePreviews(sub), "unchanged review map was republished")

		if err := os.WriteFile(path, []byte("changed\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		advanceExecCleanupPreviewTicker(t)
		events := broker.takePreviews(sub)
		changed := assertExecCleanupPreview(t, events, previewID, "RUNNING · observed so far")
		if len(changed.Files) != 1 || changed.Files[0].BeforePath != path || changed.Files[0].AfterPath != path {
			t.Fatalf("changed review files = %+v", changed.Files)
		}
		advanceExecCleanupPreviewTicker(t)
		assertNoExecCleanupPreview(t, broker.takePreviews(sub), "unchanged changed-review map was republished")

		if err := os.WriteFile(path, []byte("original\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		advanceExecCleanupPreviewTicker(t)
		assertExecCleanupPreview(t, broker.takePreviews(sub), previewID, "")

		cancel()
		synctest.Wait()
		assertNoExecCleanupPreview(t, broker.takePreviews(sub), "cancellation republished an already removed preview")
		broker.mu.Lock()
		_, active := broker.previews[previewID]
		broker.mu.Unlock()
		if active {
			t.Errorf("preview %q remained active after cancellation", previewID)
		}
	})
}

func TestExecScopePreviewRetriesAfterBrokerAdmissionRejection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "tracked.txt")
		if err := os.WriteFile(path, []byte("original\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		before := snapshotExecFile(path, nil)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		broker := newLiveDiffBroker(ctx)
		broker.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{root: {"thread": true}}})
		for index := range 16 {
			broker.publishPreview(liveDiffPreview{
				ID: filepath.Join("occupied", string(rune('a'+index))), Workspace: root, Thread: "thread", Status: "RUNNING · occupied",
			}, false)
		}
		broker.mu.Lock()
		occupiedCount := len(broker.previews)
		broker.mu.Unlock()
		if occupiedCount != 16 {
			t.Fatalf("occupied preview count = %d, want 16", occupiedCount)
		}
		sub := broker.subscribe()
		seed := broker.takePreviews(sub)
		seeded := 0
		for _, event := range seed {
			if event.Preview != nil {
				seeded++
			}
		}
		if seeded != 16 {
			t.Fatalf("subscriber seed contained %d active previews, want 16", seeded)
		}

		const previewID = "running:admission-retry"
		go runExecScopePreview(ctx, broker, execObservation{Class: execScoped.String(), Files: []execFileSnapshot{before}}, liveDiffPreview{
			ID: previewID, Workspace: root, Thread: "thread",
		})
		synctest.Wait()
		assertNoExecCleanupPreview(t, broker.takePreviews(sub), "unchanged scope should not seek broker admission")
		assertExecCleanupPreviewNotActive(t, broker, previewID)

		if err := os.WriteFile(path, []byte("changed\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		advanceExecCleanupPreviewTicker(t)
		assertNoExecCleanupPreview(t, broker.takePreviews(sub), "broker-full changed preview should be rejected")
		assertExecCleanupPreviewNotActive(t, broker, previewID)

		broker.publishPreview(liveDiffPreview{ID: "occupied/a"}, true)
		assertExecCleanupPreview(t, broker.takePreviews(sub), "occupied/a", "")
		advanceExecCleanupPreviewTicker(t) // The file is unchanged since the rejected preview; retry it.
		preview := assertExecCleanupPreview(t, broker.takePreviews(sub), previewID, "RUNNING · observed so far")
		if len(preview.Files) != 1 {
			t.Fatalf("retried preview lost its changed-file projection: %+v", preview)
		}
		broker.mu.Lock()
		active, found := broker.previews[previewID]
		count := len(broker.previews)
		broker.mu.Unlock()
		if !found || active.Status != "RUNNING · observed so far" || count != 16 {
			t.Fatalf("retried preview not admitted to full broker: found=%v active=%+v count=%d", found, active, count)
		}

		cancel()
		synctest.Wait()
		assertExecCleanupPreview(t, broker.takePreviews(sub), previewID, "")
	})
}

func TestExecScopePreviewDoesNotTreatReadBudgetAsChange(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "large.txt")
		original := strings.Repeat("old\n", 400000)
		if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
			t.Fatal(err)
		}
		before := snapshotExecFile(path, nil)
		if before.Error != "" || before.watchStamp == "" {
			t.Fatalf("capture did not retain a preview metadata baseline: %+v", before)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		broker := newLiveDiffBroker(ctx)
		broker.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{root: {"thread": true}}})
		sub := broker.subscribe()
		broker.takePreviews(sub)
		const previewID = "running:large-unchanged"
		go runExecScopePreview(ctx, broker, execObservation{Class: execScoped.String(), Files: []execFileSnapshot{before}}, liveDiffPreview{
			ID: previewID, Workspace: root, Thread: "thread",
		})
		advanceExecCleanupPreviewTicker(t)
		assertNoExecCleanupPreview(t, broker.takePreviews(sub), "unchanged large file became a running change")
		broker.mu.Lock()
		_, active := broker.previews[previewID]
		broker.mu.Unlock()
		if active {
			t.Fatal("unchanged large file occupied a preview slot")
		}

		if err := os.WriteFile(path, []byte(strings.Repeat("new\n", 400000)), 0o600); err != nil {
			t.Fatal(err)
		}
		// The running preview polls a metadata stamp before attempting a
		// bounded read. A same-size rewrite can retain the same filesystem
		// timestamp when the test completes within one clock tick, so make the
		// fixture's change observable independently of timestamp resolution.
		changedTime := time.Now().Add(time.Hour)
		if err := os.Chtimes(path, changedTime, changedTime); err != nil {
			t.Fatal(err)
		}
		advanceExecCleanupPreviewTicker(t)
		changed := assertExecCleanupPreview(t, broker.takePreviews(sub), previewID, "RUNNING · observed so far")
		if len(changed.Files) != 1 || !strings.Contains(changed.Files[0].Incomplete, "content bound") {
			t.Fatalf("changed large file lost its bounded observation: %+v", changed)
		}
		cancel()
		synctest.Wait()
		assertExecCleanupPreview(t, broker.takePreviews(sub), previewID, "")
	})
}

func assertExecCleanupPreviewNotActive(t *testing.T, broker *liveDiffBroker, id string) {
	t.Helper()
	broker.mu.Lock()
	_, active := broker.previews[id]
	count := len(broker.previews)
	broker.mu.Unlock()
	if active || count != 16 {
		t.Fatalf("rejected preview is active or changed the full broker: active=%v count=%d", active, count)
	}
}

func advanceExecCleanupPreviewTicker(t *testing.T) {
	t.Helper()
	time.Sleep(500 * time.Millisecond)
	synctest.Wait()
}

func assertExecCleanupPreview(t *testing.T, events []liveDiffEvent, id, status string) liveDiffPreview {
	t.Helper()
	var previews []liveDiffPreview
	for _, event := range events {
		if event.Preview != nil {
			previews = append(previews, *event.Preview)
		}
	}
	if len(previews) != 1 {
		t.Fatalf("got %d preview events, want one: %+v", len(previews), events)
	}
	preview := previews[0]
	if preview.ID != id || preview.Status != status {
		t.Fatalf("preview = %+v, want ID %q status %q", preview, id, status)
	}
	return preview
}

func assertNoExecCleanupPreview(t *testing.T, events []liveDiffEvent, message string) {
	t.Helper()
	for _, event := range events {
		if event.Preview != nil {
			t.Fatalf("%s: %+v", message, *event.Preview)
		}
	}
}
