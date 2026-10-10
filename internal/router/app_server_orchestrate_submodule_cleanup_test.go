package router

import (
	json "encoding/json/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/orchestrate"
)

func submoduleCleanupGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSuffix(string(out), "\n")
}

func submoduleCleanupUI(t *testing.T) *appServerUI {
	t.Helper()
	leaf, module, workspace := orchestrateVCSWorkspace(t, "git"), orchestrateVCSWorkspace(t, "git"), orchestrateVCSWorkspace(t, "git")
	gitTestRun(t, module, "-c", "protocol.file.allow=always", "submodule", "add", "-q", leaf, "deep module")
	gitTestCommit(t, module)
	gitTestRun(t, workspace, "-c", "protocol.file.allow=always", "submodule", "add", "-q", module, "lib module")
	gitTestRun(t, workspace, "-c", "protocol.file.allow=always", "submodule", "update", "--init", "--recursive")
	gitTestCommit(t, workspace)
	gitTestRun(t, workspace, "config", "--file", ".gitmodules", "submodule.cold.path", "cold")
	gitTestRun(t, workspace, "config", "--file", ".gitmodules", "submodule.cold.url", "https://invalid.invalid/uninitialized")
	gitTestRun(t, workspace, "update-index", "--add", "--cacheinfo", "160000,"+submoduleCleanupGit(t, module, "rev-parse", "HEAD")+",cold")
	gitTestRun(t, workspace, "add", ".gitmodules")
	gitTestRun(t, workspace, "commit", "-qm", "uninitialized submodule")
	return orchestrateCleanupUIInWorkspace(t, workspace)
}

func acceptSubmoduleCleanup(t *testing.T, u *appServerUI) {
	t.Helper()
	if _, err := u.proxy.applyJournal(u.ctx, u.session.cwd, "main", "", []journalMutation{{Op: "set", P: "/1", State: new("accepted")}}); err != nil {
		t.Fatal(err)
	}
}

func TestAppServerOrchestrateSubmoduleCleanupMCP(t *testing.T) {
	u := submoduleCleanupUI(t)
	b := u.orchestrateThreads["child"].batch
	for i := len(b.Submodules) - 1; i >= 0; i-- {
		clone := filepath.Join(b.Checkout, b.Submodules[i].Path)
		writeTestFile(t, filepath.Join(clone, "file"), "changed batch")
		gitTestRun(t, clone, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-qam", "batch change")
	}
	gitTestRun(t, b.Checkout, "commit", "-qam", "submodule results")
	gitTestRun(t, u.session.cwd, "merge", "--ff-only", b.Branch)
	if submoduleCleanupGit(t, u.session.cwd, "rev-parse", "HEAD") != submoduleCleanupGit(t, b.Checkout, "rev-parse", "HEAD") {
		t.Fatal("source integration failed")
	}
	clone := filepath.Join(b.Checkout, b.Submodules[1].Path)
	for _, flag := range []string{"assume-unchanged", "skip-worktree"} {
		gitTestRun(t, clone, "update-index", "--"+flag, "file")
		writeTestFile(t, filepath.Join(clone, "file"), "hidden unfinished work")
		if _, err := u.proxy.applyJournal(u.ctx, u.session.cwd, "main", "", []journalMutation{{Op: "set", P: "/1", State: new("accepted")}}); err == nil {
			t.Fatal("accepted hidden submodule edits", flag)
		}
		if data, err := os.ReadFile(filepath.Join(clone, "file")); err != nil || string(data) != "hidden unfinished work" {
			t.Fatal("acceptance changed hidden submodule edits", flag, err)
		}
		gitTestRun(t, clone, "update-index", "--no-"+flag, "file")
		gitTestRun(t, clone, "checkout", "--", "file")
	}
	acceptSubmoduleCleanup(t, u)
	// Preserve dirty source work and its original checked-out branches/index.
	dirs := []string{u.session.cwd}
	var tips []string
	for _, sub := range b.Submodules {
		dirs = append(dirs, sub.Repository)
		tips = append(tips, submoduleCleanupGit(t, filepath.Join(b.Checkout, sub.Path), "rev-parse", "HEAD"))
	}
	before := make(map[string]string)
	state := func(dir string) string {
		return submoduleCleanupGit(t, dir, "branch", "--show-current") + submoduleCleanupGit(t, dir, "rev-parse", "HEAD") + submoduleCleanupGit(t, dir, "diff", "--binary", "HEAD") + submoduleCleanupGit(t, dir, "diff", "--cached", "--binary")
	}
	for _, dir := range dirs {
		writeTestFile(t, filepath.Join(dir, "file"), "source staged")
		gitTestRun(t, dir, "add", "file")
		writeTestFile(t, filepath.Join(dir, "file"), "source unstaged")
	}
	for _, dir := range dirs {
		before[dir] = state(dir)
	}
	call := orchestrateCleanupMCPClient(t, u)
	ref := "refs/mekugi/orchestrate/" + b.Branch
	nested := b.Submodules[1]
	gitTestRun(t, nested.Repository, "update-ref", ref, nested.Base)
	call("main", true)
	if _, err := os.Stat(b.Checkout); err != nil || submoduleCleanupGit(t, nested.Repository, "rev-parse", ref) != nested.Base {
		t.Fatal("retention failure discarded work or replaced a source ref", err)
	}
	gitTestRun(t, nested.Repository, "update-ref", "-d", ref, nested.Base)
	// Already integrated source refs need no duplicate retention ref.
	gitTestRun(t, b.Submodules[0].Repository, "-c", "protocol.file.allow=always", "fetch", "--no-recurse-submodules", "--no-write-fetch-head", filepath.Join(b.Checkout, b.Submodules[0].Path), tips[0]+":refs/heads/already_integrated")
	result := call("main", false)
	data, err := json.Marshal(result.StructuredContent)
	var removed orchestrate.Batch
	if err != nil || json.Unmarshal(data, &removed) != nil || removed.State != "removed" || len(removed.RetainedSubmodules) != 2 {
		t.Fatal("missing retained submodule result", string(data), err)
	}
	if _, err := os.Lstat(b.Checkout); !os.IsNotExist(err) {
		t.Fatal("submodule checkout survived", err)
	}
	for i, sub := range b.Submodules {
		retained := removed.RetainedSubmodules[i]
		if retained.Path != sub.Path || retained.Tip != tips[i] || submoduleCleanupGit(t, sub.Repository, "rev-parse", retained.Ref) != tips[i] {
			t.Fatal("changed nested commit was not retained", retained)
		}
	}
	for _, dir := range dirs {
		if after := state(dir); after != before[dir] {
			t.Fatalf("cleanup changed source branch, worktree or index %s\nbefore: %s\nafter: %s", dir, before[dir], after)
		}
	}
	reopened, err := (&orchestrate.Store{Directory: u.proxy.orchestration.store.Directory}).Snapshot(u.session.cwd, "main")
	if err != nil || len(reopened[0].RetainedSubmodules) != 2 {
		t.Fatal("submodule retention lost on reopen", reopened, err)
	}
}

func TestAppServerOrchestrateSubmoduleCleanupPreservesWork(t *testing.T) {
	u := submoduleCleanupUI(t)
	b := u.orchestrateThreads["child"].batch
	acceptSubmoduleCleanup(t, u)
	call := orchestrateCleanupMCPClient(t, u)
	clone := filepath.Join(b.Checkout, b.Submodules[1].Path)
	gitdir := filepath.Join(clone, ".git")
	gitTestRun(t, clone, "config", "user.name", "test")
	gitTestRun(t, clone, "config", "user.email", "test@example.com")
	// Parent ignore configuration must not hide unfinished nested edits.
	gitTestRun(t, b.Checkout, "config", "submodule.lib module.ignore", "all")
	gitTestRun(t, filepath.Dir(clone), "config", "submodule.deep module.ignore", "all")
	for _, flag := range []string{"assume-unchanged", "skip-worktree"} {
		gitTestRun(t, clone, "update-index", "--"+flag, "--", "file")
		writeTestFile(t, filepath.Join(clone, "file"), "hidden dirty work")
		call("main", true)
		if data, err := os.ReadFile(filepath.Join(clone, "file")); err != nil || string(data) != "hidden dirty work" {
			t.Fatal("cleanup discarded hidden tracked edits", flag, err)
		}
		gitTestRun(t, clone, "update-index", "--no-"+flag, "--", "file")
		gitTestRun(t, clone, "checkout", "--", "file")
	}
	writeTestFile(t, filepath.Join(clone, "file"), "dirty")
	call("main", true)
	gitTestRun(t, clone, "stash", "-q")
	call("main", true)
	gitTestRun(t, clone, "stash", "pop", "-q")
	gitTestRun(t, clone, "checkout", "--", "file")
	gitTestRun(t, clone, "checkout", "-qb", "unintegrated")
	writeTestFile(t, filepath.Join(clone, "file"), "unique ref")
	gitTestRun(t, clone, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-qam", "unique work")
	gitTestRun(t, clone, "tag", "unintegrated")
	gitTestRun(t, clone, "checkout", "-q", b.Branch)
	call("main", true)
	gitTestRun(t, clone, "branch", "-D", "unintegrated")
	call("main", true) // The tag alone still retains work outside the batch history.
	gitTestRun(t, clone, "tag", "-d", "unintegrated")
	moved := filepath.Join(t.TempDir(), "preserved-clone")
	if err := os.Rename(clone, moved); err != nil {
		t.Fatal(err)
	}
	call("main", true)
	if err := os.Rename(moved, clone); err != nil {
		t.Fatal(err)
	}
	storage := filepath.Join(gitdir, "modules", "deinitialized")
	writeTestFile(t, filepath.Join(storage, "HEAD"), "retained storage")
	call("main", true)
	if got, err := os.ReadFile(filepath.Join(storage, "HEAD")); err != nil || string(got) != "retained storage" {
		t.Fatal("uninspected storage was discarded", err)
	}
	// Move the fixture storage outside the checkout instead of discarding it.
	if err := os.Rename(filepath.Join(gitdir, "modules"), filepath.Join(t.TempDir(), "preserved")); err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(b.Checkout, "unknown", ".git", "HEAD")
	writeTestFile(t, unknown, "unregistered repository")
	exclude := submoduleCleanupGit(t, b.Checkout, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude")
	writeTestFile(t, exclude, "unknown/\n")
	call("main", true)
	if _, err := os.Stat(unknown); err != nil {
		t.Fatal("unregistered repository was discarded", err)
	}
	if err := os.Rename(filepath.Dir(filepath.Dir(unknown)), filepath.Join(t.TempDir(), "preserved")); err != nil {
		t.Fatal(err)
	}
	call("main", false) // Unchanged safe clones need no additional source refs.
	if len(u.orchestrateThreads["child"].batch.RetainedSubmodules) != 0 {
		t.Fatal("unchanged submodule cleanup retained extra refs")
	}
}
