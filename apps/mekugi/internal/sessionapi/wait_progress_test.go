package sessionapi

import (
	"fmt"
	"maps"
	"strings"
	"testing"

	changes "github.com/yusing/mekugi/modules/changehistory/presentation"
)

func TestWaitProgressUsesUnfilteredCapturesWithoutActivity(t *testing.T) {
	h := NewHub(t.Context(), nil)
	root := familyRuntime(h, nil, Frame{Session: Session{Thread: "root", Workspace: "/project"}, Agents: familyActivity("root", "main")})
	add := reviewChunk(2, "/root/child", "", "/project/a", "", "one\n")
	remove := reviewChunk(3, "/root/child", "/project/a", "", "one\n", "")
	scratch := reviewChunk(4, "/root/child", "", "/tmp/scratch", "", "scratch\n")
	child := familyRuntime(h, root, Frame{Session: Session{Thread: "child", Caller: "/root/child", Workspace: "/project"}, Agents: familyActivity("child", "checking tests"), Review: familyReview(add, remove, scratch)})
	familyRuntime(h, root, Frame{Session: Session{Thread: "quiet", Caller: "/root/quiet"}, Agents: familyActivity("quiet", "thinking")})
	_, diff := h.Family("root")
	if len(diff.Files) != 0 {
		t.Fatal("fixture should be invisible in the Diff pane")
	}
	snapshot := h.ProgressSnapshot("root")
	if snapshot == nil {
		t.Fatal("unfiltered snapshot unavailable")
	}
	count := 0
	for range snapshot.Publication.Progress(map[string]bool{"/root/child": true}, nil) {
		count++
	}
	if count != 3 {
		t.Fatalf("unfiltered snapshot lost captures: %d", count)
	}
	targets := []string{"/root/child", "/root/quiet"}
	progress, seen := captureProgress(t, h, "root", targets, nil)
	if len(progress) != 3 || len(seen) != 3 || progress[2].Caller != "/root/child" || progress[2].Change != scratch.Change {
		t.Fatalf("missing capture or included unchanged agent: %+v %v", progress, seen)
	}
	if repeated, cursor := captureProgress(t, h, "root", targets, seen); len(repeated) != 0 || len(cursor) != 0 {
		t.Fatal("unchanged captures repeated")
	}
	late := reviewChunk(1, "/root/child", "", "/project/late", "", "late\n")
	extension := reviewChunk(5, "/root/child", "", "/project/extended", "", "extended\n")
	extension.Change = add.Change
	frame := *child.server.frame.Load()
	frame.Review = familyReview(add, remove, scratch, late, extension)
	frame.Review.Revision++
	frame.Agents = familyActivity("child", "running follow-up")
	child.server.frame.Store(&frame)
	next, cursor := captureProgress(t, h, "root", targets, seen)
	if len(next) != 2 || len(cursor) != 2 || next[0].Change != late.Change || next[1].Change != extension.Change {
		t.Fatalf("late or extended capture lost: %+v %v", next, cursor)
	}
	maps.Copy(seen, cursor)
	if repeated, _ := captureProgress(t, h, "root", targets, seen); len(repeated) != 0 {
		t.Fatal("extended capture repeated")
	}

}

func TestWaitProgressFreezesAllSelectionsBeforeCombinedOutputBounding(t *testing.T) {
	h := NewHub(t.Context(), nil)
	var captures []changes.Chunk
	for i := range 3000 {
		captures = append(captures, reviewChunk(uint64(i+1), "/root/child", "", fmt.Sprintf("/project/%04d-%s", i, strings.Repeat("長", 150)), "", "line\n"))
	}
	root := familyRuntime(h, nil, Frame{Session: Session{Thread: "root", Workspace: "/project"}})
	familyRuntime(h, root, Frame{Session: Session{Thread: "child", Caller: "/root/child", Workspace: "/project"}, Agents: familyActivity("child", strings.Repeat("activity", 500)), Review: familyReview(captures...)})
	progress, cursor := captureProgress(t, h, "root", []string{"/root/child"}, nil)
	if len(cursor) != len(captures) || len(progress) != len(captures) || progress[len(progress)-1].Change != captures[len(captures)-1].Change {
		t.Fatalf("freeze dropped selections: captures=%d cursor=%d", len(progress), len(cursor))
	}

}

func TestWaitProgressIncludesRetainedGitDiagnosticsWithoutAuthoredCounts(t *testing.T) {
	h := NewHub(t.Context(), nil)
	git := reviewChunk(1, "/root/child", "/project/a", "/project/a", "old\n", "new\n")
	git.Review.Origin = "git checkout"
	builder := changes.NewBuilder(nil)
	if err := builder.Add(changes.Attempt{AttemptIdentity: changes.AttemptIdentity{Key: changes.AttemptKey{Workspace: "/project", Call: "git"}, Change: git.Change}, Chunks: []changes.Chunk{git}}); err != nil {
		t.Fatal(err)
	}
	snapshot := builder.Finish()
	view := &changes.ReviewView{Publication: snapshot.Publication, Workspace: "/project", Coverage: "complete", Revision: 1}
	root := familyRuntime(h, nil, Frame{Session: Session{Thread: "root", Workspace: "/project"}, Review: view})
	familyRuntime(h, root, Frame{Session: Session{Thread: "child", Caller: "/root/child", Workspace: "/project"}, Agents: familyActivity("child", "checking out"), Review: view})
	progress, cursor := captureProgress(t, h, "root", []string{"/root/child"}, nil)
	if len(progress) != 1 || len(cursor) != 1 || progress[0].Change != git.Change {
		t.Fatalf("Git capture missing or duplicated: %+v", progress)
	}
	_, diff := h.Family("root")
	if len(diff.Files) != 0 || len(diff.Counts.Callers) != 0 {
		t.Fatal("diagnostic capture became an authored edit")
	}
	next := changes.NewBuilder(snapshot)
	git.Key, git.Change, git.CaptureOrder = "git-2", "amber2", 2
	if err := next.Add(changes.Attempt{AttemptIdentity: changes.AttemptIdentity{Key: changes.AttemptKey{Workspace: "/project", Call: "git-2"}, Change: git.Change}, Chunks: []changes.Chunk{git}}); err != nil {
		t.Fatal(err)
	}
	if len(next.Finish().Publication.Diagnostics) != 2 || len(snapshot.Publication.Diagnostics) != 1 {
		t.Fatal("new diagnostic mutated its prior publication")
	}
}

func TestWaitProgressKeepsDiagnosticsAcrossAuthoredAppend(t *testing.T) {
	authored := reviewChunk(1, "/root/child", "/project/a", "/project/a", "one\n", "two\n")
	git := reviewChunk(2, "/root/child", "/project/b", "/project/b", "old\n", "new\n")
	git.Review.Origin = "git checkout"
	add := func(builder *changes.Builder, chunk changes.Chunk) {
		t.Helper()
		if err := builder.Add(changes.Attempt{AttemptIdentity: changes.AttemptIdentity{Key: changes.AttemptKey{Workspace: "/project", Call: chunk.Key}, Change: chunk.Change}, Chunks: []changes.Chunk{chunk}}); err != nil {
			t.Fatal(err)
		}
	}
	builder := changes.NewBuilder(nil)
	add(builder, authored)
	add(builder, git)
	base := builder.Finish()
	next := changes.NewBuilder(base)
	another := reviewChunk(3, "/root/child", "/project/c", "/project/c", "old\n", "new\n")
	another.Review.Origin = "git checkout"
	add(next, another)
	add(next, reviewChunk(4, "/root/child", "/project/a", "/project/a", "two\n", "three\n"))
	updated := next.Finish()
	h := NewHub(t.Context(), nil)
	root := familyRuntime(h, nil, Frame{Session: Session{Thread: "root", Workspace: "/project"}})
	familyRuntime(h, root, Frame{Session: Session{Thread: "child", Caller: "/root/child", Workspace: "/project"}, Agents: familyActivity("child", "editing"), Review: &changes.ReviewView{Publication: updated.Publication, Workspace: "/project", Coverage: "complete", Revision: 1}})
	progress, _ := captureProgress(t, h, "root", []string{"/root/child"}, nil)
	found := map[string]bool{}
	for _, capture := range progress {
		found[capture.Change] = true
	}
	if !found[git.Change] || !found[another.Change] || len(base.Publication.Diagnostics) != 1 {
		t.Fatalf("authored append lost diagnostics: %+v", progress)
	}
}

func captureProgress(t *testing.T, h *Hub, thread string, targets []string, seen map[string]int) ([]changes.CaptureProgress, map[string]int) {
	t.Helper()
	snapshot := h.ProgressSnapshot(thread)
	if snapshot == nil {
		t.Fatal("capture publication unavailable")
	}
	callers := make(map[string]bool, len(targets))
	for _, target := range targets {
		callers[target] = true
	}
	var captures []changes.CaptureProgress
	cursor := map[string]int{}
	for capture := range snapshot.Publication.Progress(callers, seen) {
		captures = append(captures, capture)
		cursor[capture.Key] = capture.Chunks
	}
	return captures, cursor
}
