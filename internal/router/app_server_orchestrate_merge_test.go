package router

import (
	"context"
	json "encoding/json/v2"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/orchestrate"
)

func TestOrchestrateShadowMergeMCP(t *testing.T) {
	workspace := t.TempDir()
	base := "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\n"
	writeTestFile(t, filepath.Join(workspace, "file.txt"), base)
	writeTestFile(t, filepath.Join(workspace, "remove"), "base")
	writeTestFile(t, filepath.Join(workspace, "binary"), "base\x00bytes")
	writeTestFile(t, filepath.Join(workspace, "script"), "run")
	writeTestFile(t, filepath.Join(workspace, ".gitattributes"), "*.txt merge=forbidden\n")
	u := orchestrateCleanupUIInWorkspace(t, workspace)
	store, child := u.proxy.orchestration.store, u.orchestrateThreads["child"]
	b := child.batch
	gitTestRun(t, b.Cwd, "config", "user.name", "test")
	gitTestRun(t, b.Cwd, "config", "user.email", "test@example.invalid")
	writeTestFile(t, filepath.Join(b.Cwd, "file.txt"), strings.Replace(base, "one", "child", 1))
	writeTestFile(t, filepath.Join(b.Cwd, "added"), "child\x00bytes")
	if err := os.Chmod(filepath.Join(b.Cwd, "script"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("file.txt", filepath.Join(b.Cwd, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(b.Cwd, "remove")); err != nil {
		t.Fatal(err)
	}
	gitTestRun(t, b.Cwd, "config", "merge.forbidden.driver", "false")
	gitTestCommit(t, b.Cwd)
	sourceText := strings.Replace(base, "nine", "source", 1)
	writeTestFile(t, filepath.Join(workspace, "file.txt"), sourceText)
	writeTestFile(t, filepath.Join(workspace, "binary"), "source\x00bytes")
	call := orchestrateTargetMCPClient(t, u, "integrate")
	call("child", true)
	orchestrateTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"child","turn":{"id":"next"}}}`)
	call("main", true)
	orchestrateTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"child","turn":{"id":"next","status":"completed"}}}`)
	gitTestRun(t, b.Cwd, "update-index", "--assume-unchanged", "file.txt")
	writeTestFile(t, filepath.Join(b.Cwd, "file.txt"), "hidden unfinished work")
	call("main", true)
	if data, err := os.ReadFile(filepath.Join(workspace, "file.txt")); err != nil || string(data) != sourceText {
		t.Fatal("rejected shadow merge changed source", string(data), err)
	}
	gitTestRun(t, b.Cwd, "update-index", "--no-assume-unchanged", "file.txt")
	gitTestRun(t, b.Cwd, "checkout", "--", "file.txt")
	result := call("main", false)
	data, err := json.Marshal(result.StructuredContent)
	if err != nil || json.Unmarshal(data, &b) != nil || b.ShadowMerge == nil {
		t.Fatal("MCP lost merge plan", result, err)
	}
	plan := b.ShadowMerge
	if plan.State != "applied" || !reflect.DeepEqual(plan.Paths, []string{"added", "file.txt", "link", "remove", "script"}) || len(plan.Conflicts) != 0 {
		t.Fatal("unexpected merge plan", plan)
	}
	for name, want := range map[string]string{"file.txt": strings.Replace(sourceText, "one", "child", 1), "binary": "source\x00bytes", "added": "child\x00bytes"} {
		if got, err := os.ReadFile(filepath.Join(workspace, name)); err != nil || string(got) != want {
			t.Fatal("writeback lost merged or unrelated source bytes", name, string(got), err)
		}
	}
	if _, err := os.Lstat(filepath.Join(workspace, "remove")); !os.IsNotExist(err) {
		t.Fatal("writeback did not delete planned path", err)
	}
	if got, err := os.Readlink(filepath.Join(workspace, "link")); err != nil || got != "file.txt" {
		t.Fatal("writeback lost symlink", got, err)
	}
	if info, err := os.Stat(filepath.Join(workspace, "script")); err != nil || info.Mode().Perm()&0111 == 0 {
		t.Fatal("writeback lost executable state", info, err)
	}
	reopened := &orchestrate.Store{Directory: store.Directory, ShadowSnapshot: orchestrateShadowSnapshot}
	gitTestRun(t, b.Cwd, "gc", "--prune=now", "--quiet")
	retained, err := reopened.List(t.Context(), workspace, "main")
	if err != nil || len(retained) != 1 || !reflect.DeepEqual(retained[0].ShadowMerge, plan) || retained[0].Integration == nil {
		t.Fatal("restart lost writeback proof", retained, err)
	}
	if _, err := exec.Command("git", "--git-dir="+b.Repository, "cat-file", "blob", retained[0].ShadowMerge.Tree+":file.txt").Output(); err != nil {
		t.Fatal("private GC removed the retained merged result", err)
	}
	gitTestRun(t, b.Cwd, "update-index", "--skip-worktree", "file.txt")
	writeTestFile(t, filepath.Join(b.Cwd, "file.txt"), "hidden unfinished work")
	call("main", true) // An applied plan still checks the checkout before writeback.
	if data, err := os.ReadFile(filepath.Join(b.Cwd, "file.txt")); err != nil || string(data) != "hidden unfinished work" {
		t.Fatal("rejected shadow writeback changed hidden edits", string(data), err)
	}
	gitTestRun(t, b.Cwd, "update-index", "--no-skip-worktree", "file.txt")
	gitTestRun(t, b.Cwd, "checkout", "--", "file.txt")
	call("main", false) // Repeat verifies postimages without replacing files.
	if _, err := u.proxy.applyJournal(u.ctx, workspace, "main", "", []journalMutation{{Op: "set", P: "/1", State: new("accepted")}}); err != nil {
		t.Fatal("confirmed shadow writeback did not authorize Main acceptance", err)
	}
	// Reopening requires current source proof, not just the retained manifest.
	if _, err := u.proxy.applyJournal(u.ctx, workspace, "main", "", []journalMutation{{Op: "set", P: "/1", State: new("working")}}); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(workspace, "file.txt"), "later source edit")
	call("main", true)
	if _, err := store.RecordIntegration(t.Context(), workspace, "main", "batch", "child"); err == nil {
		t.Fatal("changed source authorized integration acceptance")
	}
	if _, _, err := store.BeginDelivery(t.Context(), workspace, "main", orchestrate.Delivery{ID: "queued", From: "main", Target: "child", Message: "continue", Deferred: true}); err != nil {
		t.Fatal(err)
	}
	call("main", true)
}

func TestOrchestrateShadowMergeConflictPaths(t *testing.T) {
	workspace := t.TempDir()
	name := "binary\nname"
	writeTestFile(t, filepath.Join(workspace, name), "base\x00bytes")
	u := orchestrateCleanupUIInWorkspace(t, workspace)
	b := u.orchestrateThreads["child"].batch
	gitTestRun(t, b.Cwd, "config", "user.name", "test")
	gitTestRun(t, b.Cwd, "config", "user.email", "test@example.invalid")
	writeTestFile(t, filepath.Join(b.Cwd, name), "child\x00bytes")
	gitTestCommit(t, b.Cwd)
	writeTestFile(t, filepath.Join(workspace, name), "source\x00bytes")
	call := orchestrateTargetMCPClient(t, u, "integrate")
	result := call("main", false)
	data, err := json.Marshal(result.StructuredContent)
	if err != nil || json.Unmarshal(data, &b) != nil || b.ShadowMerge.State != "conflicted" || !reflect.DeepEqual(b.ShadowMerge.Conflicts, []string{name}) {
		t.Fatal("binary conflict path was lost", b, err)
	}
	if got, err := os.ReadFile(filepath.Join(workspace, name)); err != nil || string(got) != "source\x00bytes" {
		t.Fatal("MCP conflict wrote source", string(got), err)
	}
	// Uncommitted batch input cannot enter the merge.
	writeTestFile(t, filepath.Join(b.Cwd, "unfinished"), "retain")
	call("main", true)
}

func TestOrchestrateShadowMergePublicationFailure(t *testing.T) {
	workspace := t.TempDir()
	writeTestFile(t, filepath.Join(workspace, "file"), "source")
	u := orchestrateCleanupUIInWorkspace(t, workspace)
	s := u.proxy.orchestration.store
	s.ShadowSnapshot = func(ctx context.Context, source, repository string) (string, error) {
		tip, err := orchestrateShadowSnapshot(ctx, source, repository)
		if err != nil {
			return "", err
		}
		paths, err := filepath.Glob(filepath.Join(s.Directory, "*", "*.json"))
		if err != nil || len(paths) != 1 {
			t.Fatal("missing fixture manifest", paths, err)
		}
		if err := os.Rename(paths[0], paths[0]+".saved"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(paths[0], 0700); err != nil {
			t.Fatal(err)
		}
		return tip, nil
	}
	if _, err := s.PlanShadowMerge(t.Context(), workspace, "main", "batch", "child"); err == nil {
		t.Fatal("failed publication returned a usable plan")
	}
	if got, err := os.ReadFile(filepath.Join(workspace, "file")); err != nil || string(got) != "source" {
		t.Fatal("publication failure changed source", string(got), err)
	}
}
