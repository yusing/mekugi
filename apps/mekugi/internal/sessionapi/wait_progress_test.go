package sessionapi

import (
	json "encoding/json/v2"
	"fmt"
	"maps"
	"strings"
	"testing"

	changes "github.com/yusing/mekugi/modules/changehistory/presentation"
)

func TestWaitProgressUsesUnfilteredCapturesAndCurrentActivity(t *testing.T) {
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
	targets := []string{"/root/child", "/root/quiet"}
	progress, seen := h.WaitProgress("root", targets, nil)
	if len(progress.Agents) != 1 || len(seen) != 3 || progress.Agents[0].Task != "/root/child" || progress.Agents[0].Status == "" || progress.Agents[0].Activity != "checking tests" || !strings.Contains(progress.Agents[0].Changes, scratch.Change) {
		t.Fatalf("missing capture/status or included unchanged agent: %+v %v", progress, seen)
	}
	if repeated, cursor := h.WaitProgress("root", targets, seen); len(repeated.Agents) != 0 || len(cursor) != 0 {
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
	next, cursor := h.WaitProgress("root", targets, seen)
	if len(next.Agents) != 1 || len(cursor) != 2 || next.Agents[0].Activity != "running follow-up" || !strings.Contains(next.Agents[0].Changes, late.Change) || !strings.Contains(next.Agents[0].Changes, "extended") {
		t.Fatalf("late or extended capture was lost: %+v %v", next, cursor)
	}
	maps.Copy(seen, cursor)
	if repeated, _ := h.WaitProgress("root", targets, seen); len(repeated.Agents) != 0 {
		t.Fatal("extended capture repeated")
	}
}

func TestWaitProgressBoundsOutputWithoutConsumingOmittedCaptures(t *testing.T) {
	h := NewHub(t.Context(), nil)
	var captures []changes.Chunk
	for i := range 300 {
		captures = append(captures, reviewChunk(uint64(i+1), "/root/child", "", fmt.Sprintf("/project/%04d-%s", i, strings.Repeat("長", 150)), "", "line\n"))
	}
	root := familyRuntime(h, nil, Frame{Session: Session{Thread: "root", Workspace: "/project"}})
	familyRuntime(h, root, Frame{Session: Session{Thread: "child", Caller: "/root/child", Workspace: "/project"}, Agents: familyActivity("child", strings.Repeat("activity", 500)), Review: familyReview(captures...)})
	seen := make(map[string]int)
	for len(seen) < len(captures) {
		progress, cursor := h.WaitProgress("root", []string{"/root/child"}, seen)
		raw, err := json.Marshal(progress)
		if err != nil || len(raw) > (12<<10)+20 || len(cursor) == 0 || !progress.More && len(seen)+len(cursor) != len(captures) {
			t.Fatalf("invalid bounded progress: bytes=%d cursor=%d seen=%d more=%t err=%v", len(raw), len(cursor), len(seen), progress.More, err)
		}
		maps.Copy(seen, cursor)
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
	progress, cursor := h.WaitProgress("root", []string{"/root/child"}, nil)
	if len(progress.Agents) != 1 || len(cursor) != 1 || !strings.Contains(progress.Agents[0].Changes, "mchanges "+git.Change+" --history") || !strings.Contains(progress.Agents[0].Changes, "diagnostic: git checkout") {
		t.Fatalf("Git capture missing provenance or duplicated: %+v", progress)
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
	progress, _ := h.WaitProgress("root", []string{"/root/child"}, nil)
	if len(progress.Agents) != 1 || !strings.Contains(progress.Agents[0].Changes, git.Change+" --history") || !strings.Contains(progress.Agents[0].Changes, another.Change+" --history") || len(base.Publication.Diagnostics) != 1 {
		t.Fatalf("authored append lost or mutated diagnostics: %+v", progress)
	}
}
