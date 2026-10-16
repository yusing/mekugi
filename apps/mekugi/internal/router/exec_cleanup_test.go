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

	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"

	"github.com/yusing/mekugi/libraries/commandanalysis"
	"github.com/yusing/mekugi/libraries/commandanalysis/execscope"
	"github.com/yusing/mekugi/libraries/diffmodel"
	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

func TestMergeExecObservationsDoesNotMutateFirstMember(t *testing.T) {
	commands := []commandanalysis.Command{{Command: "first"}, {Command: "command spare"}}
	labels := []string{"shared", "label spare"}
	programs := []execscope.Program{{Label: "shared"}, {Label: "program spare"}}
	roots := []string{"/workspace/project", "/root spare"}
	files := []changecapture.File{{Path: "/workspace/first"}, {Path: "/file spare"}}
	omitted := []changecapture.Omission{{Path: "/workspace/omitted"}, {Path: "/omission spare"}}
	listings := []changecapture.Listing{{Root: "/workspace/listed"}, {Root: "/listing spare"}}
	excluded := []string{"/workspace/excluded", "/excluded spare"}
	first := &changecapture.Observation{
		Commands: commands[:1], Class: execscope.Scoped.String(), Reason: "first reason", Labels: labels[:1],
		Files: files[:1], Omitted: omitted[:1], Listings: listings[:1], Roots: roots[:1],
		Programs: programs[:1], Excluded: excluded[:1], WindowStart: time.Unix(30, 0),
	}
	second := &changecapture.Observation{
		Commands: []commandanalysis.Command{{Command: "second"}}, Class: execscope.Opaque.String(), Reason: "stronger reason",
		Labels: []string{"shared", "second"}, Programs: []execscope.Program{{Label: "shared"}, {Label: "second"}},
		Roots: []string{"/workspace", "/outside"}, Files: []changecapture.File{{Path: "/workspace/second"}},
		Omitted: []changecapture.Omission{{Path: "/workspace/omitted-2"}}, Listings: []changecapture.Listing{{Root: "/workspace/listed-2"}},
		Excluded: []string{"/workspace/excluded-2"}, WindowStart: time.Unix(20, 0), CodeMode: true,
	}
	third := &changecapture.Observation{
		Commands: []commandanalysis.Command{{Command: "third"}}, Labels: []string{"third"},
		Programs: []execscope.Program{{Label: "third"}}, Roots: []string{"/workspace/child", "/last"},
		Files: []changecapture.File{{Path: "/workspace/third"}}, WindowStart: time.Unix(10, 0),
	}
	firstBefore := *first
	firstBefore.Commands = append([]commandanalysis.Command(nil), first.Commands...)
	firstBefore.Labels = append([]string(nil), first.Labels...)
	firstBefore.Programs = append([]execscope.Program(nil), first.Programs...)
	firstBefore.Roots = append([]string(nil), first.Roots...)
	firstBefore.Files = append([]changecapture.File(nil), first.Files...)
	firstBefore.Omitted = append([]changecapture.Omission(nil), first.Omitted...)
	firstBefore.Listings = append([]changecapture.Listing(nil), first.Listings...)
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
	if got, want := merged.Commands, []commandanalysis.Command{{Command: "first"}, {Command: "second"}, {Command: "third"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("commands = %+v, want %+v", got, want)
	}
	if got, want := merged.Labels, []string{"shared", "second", "third"}; !reflect.DeepEqual(got, want) {
		t.Errorf("labels = %v, want %v", got, want)
	}
	if got, want := merged.Programs, []execscope.Program{{Label: "shared"}, {Label: "second"}, {Label: "third"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("programs = %+v, want %+v", got, want)
	}
	if got, want := merged.Roots, []string{"/workspace", "/outside", "/last"}; !reflect.DeepEqual(got, want) {
		t.Errorf("roots = %v, want %v", got, want)
	}
	if got, want := merged.Files, []changecapture.File{{Path: "/workspace/first"}, {Path: "/workspace/second"}, {Path: "/workspace/third"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("files = %+v, want %+v", got, want)
	}
	if got, want := merged.Omitted, []changecapture.Omission{{Path: "/workspace/omitted"}, {Path: "/workspace/omitted-2"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("omitted = %+v, want %+v", got, want)
	}
	if got, want := merged.Listings, []changecapture.Listing{{Root: "/workspace/listed"}, {Root: "/workspace/listed-2"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("listings = %+v, want %+v", got, want)
	}
	if got, want := merged.Excluded, []string{"/workspace/excluded", "/workspace/excluded-2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("excluded = %v, want %v", got, want)
	}
	if merged.Class != execscope.Opaque.String() || merged.Reason != "stronger reason" || !merged.CodeMode || !merged.WindowStart.Equal(time.Unix(10, 0)) {
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
		before := changecapture.ReadFile(path, nil)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		broker := newLivePreview(ctx)
		setLiveScope(broker, previewmodel.Scope{Workspaces: map[string]map[string]bool{root: {"thread": true}}})
		sub := testReview(broker).Subscribe()
		testReview(broker).TakePreviews(sub) // Discard initial scope synchronization.
		const previewID = "running:cleanup-test"
		go runExecScopePreview(ctx, broker, changecapture.Observation{Class: execscope.Scoped.String(), Files: []changecapture.File{before}}, previewmodel.Preview{
			ID: previewID, Workspace: root, Thread: "thread",
		}, nil, nil)
		synctest.Wait()
		assertNoExecCleanupPreview(t, testReview(broker).TakePreviews(sub), "unchanged scope should stay out of the pane")

		advanceExecCleanupPreviewTicker(t)
		assertNoExecCleanupPreview(t, testReview(broker).TakePreviews(sub), "unchanged scope should stay out of the pane after polling")
		advanceExecCleanupPreviewTicker(t)
		assertNoExecCleanupPreview(t, testReview(broker).TakePreviews(sub), "unchanged review map was republished")

		if err := os.WriteFile(path, []byte("changed\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		advanceExecCleanupPreviewTicker(t)
		events := testReview(broker).TakePreviews(sub)
		changed := assertExecCleanupPreview(t, events, previewID, previewmodel.PreviewRunning)
		if len(changed.Files) != 1 || changed.Files[0].BeforePath != path || changed.Files[0].AfterPath != path {
			t.Fatalf("changed review files = %+v", changed.Files)
		}
		advanceExecCleanupPreviewTicker(t)
		assertNoExecCleanupPreview(t, testReview(broker).TakePreviews(sub), "unchanged changed-review map was republished")

		if err := os.WriteFile(path, []byte("original\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		advanceExecCleanupPreviewTicker(t)
		assertExecCleanupPreview(t, testReview(broker).TakePreviews(sub), previewID, "")

		cancel()
		synctest.Wait()
		assertNoExecCleanupPreview(t, testReview(broker).TakePreviews(sub), "cancellation republished an already removed preview")
		_, active := currentLivePreview(broker, previewID)
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
		before := changecapture.ReadFile(path, nil)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		broker := newLivePreview(ctx)
		setLiveScope(broker, previewmodel.Scope{Workspaces: map[string]map[string]bool{root: {"thread": true}}})
		for index := range 16 {
			broker.Update(previewmodel.Preview{
				ID: filepath.Join("occupied", string(rune('a'+index))), Workspace: root, Thread: "thread", Status: "RUNNING · occupied",
				Files: []diffmodel.ReviewFile{diffmodel.RenderReviewPreviewFile("", path, "", "occupied\n")},
			}, false)
		}
		occupiedCount := len(broker.Current())
		if occupiedCount != 16 {
			t.Fatalf("occupied preview count = %d, want 16", occupiedCount)
		}
		sub := testReview(broker).Subscribe()
		seed := testReview(broker).TakePreviews(sub)
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
		go runExecScopePreview(ctx, broker, changecapture.Observation{Class: execscope.Scoped.String(), Files: []changecapture.File{before}}, previewmodel.Preview{
			ID: previewID, Workspace: root, Thread: "thread",
		}, nil, nil)
		synctest.Wait()
		assertNoExecCleanupPreview(t, testReview(broker).TakePreviews(sub), "unchanged scope should not seek broker admission")
		assertExecCleanupPreviewNotActive(t, broker, previewID)

		if err := os.WriteFile(path, []byte("changed\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		advanceExecCleanupPreviewTicker(t)
		assertNoExecCleanupPreview(t, testReview(broker).TakePreviews(sub), "broker-full changed preview should be rejected")
		assertExecCleanupPreviewNotActive(t, broker, previewID)

		broker.Update(previewmodel.Preview{ID: "occupied/a"}, true)
		assertExecCleanupPreview(t, testReview(broker).TakePreviews(sub), "occupied/a", "")
		advanceExecCleanupPreviewTicker(t) // The file is unchanged since the rejected preview; retry it.
		preview := assertExecCleanupPreview(t, testReview(broker).TakePreviews(sub), previewID, previewmodel.PreviewRunning)
		if len(preview.Files) != 1 {
			t.Fatalf("retried preview lost its changed-file projection: %+v", preview)
		}
		active, found := currentLivePreview(broker, previewID)
		count := len(broker.Current())
		if !found || active.Status != previewmodel.PreviewRunning || count != 16 {
			t.Fatalf("retried preview not admitted to full broker: found=%v active=%+v count=%d", found, active, count)
		}

		cancel()
		synctest.Wait()
		assertExecCleanupPreview(t, testReview(broker).TakePreviews(sub), previewID, "")
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
		before := changecapture.ReadFile(path, nil)
		if before.Error != "" || before.WatchStamp == "" {
			t.Fatalf("capture did not retain a preview metadata baseline: %+v", before)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		broker := newLivePreview(ctx)
		setLiveScope(broker, previewmodel.Scope{Workspaces: map[string]map[string]bool{root: {"thread": true}}})
		sub := testReview(broker).Subscribe()
		testReview(broker).TakePreviews(sub)
		const previewID = "running:large-unchanged"
		go runExecScopePreview(ctx, broker, changecapture.Observation{Class: execscope.Scoped.String(), Files: []changecapture.File{before}}, previewmodel.Preview{
			ID: previewID, Workspace: root, Thread: "thread",
		}, nil, nil)
		advanceExecCleanupPreviewTicker(t)
		assertNoExecCleanupPreview(t, testReview(broker).TakePreviews(sub), "unchanged large file became a running change")
		_, active := currentLivePreview(broker, previewID)
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
		changed := assertExecCleanupPreview(t, testReview(broker).TakePreviews(sub), previewID, "")
		if len(changed.Files) != 0 {
			t.Fatalf("unavailable large content opened a preview: %+v", changed)
		}
		cancel()
		synctest.Wait()
		assertNoExecCleanupPreview(t, testReview(broker).TakePreviews(sub), "cancellation republished a hidden preview")
	})
}

func assertExecCleanupPreviewNotActive(t *testing.T, broker *previewmodel.Session, id string) {
	t.Helper()
	_, active := currentLivePreview(broker, id)
	count := len(broker.Current())
	if active || count != 16 {
		t.Fatalf("rejected preview is active or changed the full broker: active=%v count=%d", active, count)
	}
}

func advanceExecCleanupPreviewTicker(t *testing.T) {
	t.Helper()
	time.Sleep(500 * time.Millisecond)
	synctest.Wait()
}

func assertExecCleanupPreview(t *testing.T, events []reviewEvent, id, status string) previewmodel.Preview {
	t.Helper()
	var previews []previewmodel.Preview
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

func assertNoExecCleanupPreview(t *testing.T, events []reviewEvent, message string) {
	t.Helper()
	for _, event := range events {
		if event.Preview != nil {
			t.Fatalf("%s: %+v", message, *event.Preview)
		}
	}
}
