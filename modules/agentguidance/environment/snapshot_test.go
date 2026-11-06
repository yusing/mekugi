package environment

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func git(t *testing.T, workspace string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = workspace
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_COUNT=0", "GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@invalid", "GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@invalid")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func repository(t *testing.T) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_PARAMETERS", "")
	workspace := t.TempDir()
	git(t, workspace, "init", "-q", "-b", "topic")
	return workspace
}

func TestSnapshotHeadRemoteContainment(t *testing.T) {
	workspace := repository(t)
	git(t, workspace, "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "base")
	base := git(t, workspace, "rev-parse", "HEAD")
	git(t, workspace, "update-ref", "refs/remotes/origin/behind", base)
	git(t, workspace, "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "new")
	head := git(t, workspace, "rev-parse", "HEAD")
	git(t, workspace, "update-ref", "refs/remotes/origin/main", head)
	git(t, workspace, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	git(t, workspace, "checkout", "--detach", "-q", base)
	snapshot := Acquire(t.Context(), workspace)
	if !slices.Equal(snapshot.Git.HeadRemotes, []string{"origin/behind", "origin/main"}) || !strings.Contains(snapshot.Text(workspace), "HEAD in local remote-tracking branches: origin/behind, origin/main") {
		t.Fatalf("detached ancestor containment: %+v", snapshot)
	}
	git(t, workspace, "checkout", "-q", "topic")
	for n := range 20 {
		git(t, workspace, "update-ref", fmt.Sprintf("refs/remotes/many/%02d", n), head)
	}
	snapshot = Acquire(t.Context(), workspace)
	if len(snapshot.Git.HeadRemotes) != 16 || !slices.Contains(snapshot.Incomplete, "head_remotes truncated") {
		t.Fatalf("remote bound: %+v", snapshot)
	}
}

func TestSnapshotEffectiveHooksPath(t *testing.T) {
	workspace := repository(t)
	for _, scope := range []string{"SYSTEM", "GLOBAL"} {
		path := filepath.Join(t.TempDir(), "config")
		if err := os.WriteFile(path, []byte("[core]\n hooksPath = "+strings.ToLower(scope)+"-hooks\n[user]\n email = private@invalid\n"), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("GIT_CONFIG_"+scope, path)
		if scope == "SYSTEM" {
			t.Setenv("GIT_CONFIG_NOSYSTEM", "0")
		}
		snapshot := Acquire(t.Context(), workspace)
		if snapshot.Git.HooksPath != strconv.Quote(strings.ToLower(scope)+"-hooks") || strings.Contains(snapshot.Text(workspace), "private@invalid") {
			t.Fatalf("effective %s config: %+v", scope, snapshot)
		}
	}
	git(t, workspace, "config", "core.hooksPath", ".hooks\nquoted\"path")
	snapshot := Acquire(t.Context(), workspace)
	if snapshot.Git.HooksPath != strconv.Quote(".hooks\nquoted\"path") {
		t.Fatalf("local override: %+v", snapshot)
	}
	git(t, workspace, "config", "core.hooksPath", strings.Repeat("界", 300))
	snapshot = Acquire(t.Context(), workspace)
	if len(snapshot.Git.HooksPath) > 514 || !slices.Contains(snapshot.Incomplete, "hooks_path truncated") {
		t.Fatalf("hooks bound: %+v", snapshot)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(workspace, "missing"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	git(t, workspace, "config", "--unset", "core.hooksPath")
	snapshot = Acquire(t.Context(), workspace)
	if snapshot.Git.HooksPath != "unset (Git default)" {
		t.Fatalf("unset hooks: %+v", snapshot)
	}
	invalid := filepath.Join(t.TempDir(), "invalid")
	if err := os.WriteFile(invalid, []byte("[invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", invalid)
	snapshot = Acquire(t.Context(), workspace)
	if snapshot.Git.HooksPath != "unavailable" || !slices.Contains(snapshot.Incomplete, "hooks_path unavailable") || snapshot.Git.State != "repository" {
		t.Fatalf("optional config failure: %+v", snapshot)
	}
}

func TestSnapshotCountsUnlistedUnpushedCommits(t *testing.T) {
	workspace := repository(t)
	commit := func(subject string) {
		git(t, workspace, "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", subject)
	}
	commit("pushed")
	for n := range 6 {
		commit(fmt.Sprintf("local %d", n))
	}
	if snapshot := Acquire(t.Context(), workspace); snapshot.Git.MoreUnpushed != 0 || strings.Contains(snapshot.Text(workspace), "unpushed") {
		t.Fatalf("without remote-tracking branches: %+v", snapshot)
	}
	git(t, workspace, "update-ref", "refs/remotes/origin/main", "HEAD~6")
	if snapshot := Acquire(t.Context(), workspace); !strings.Contains(snapshot.Text(workspace), "local 1\n(... 1 more unpushed commit)") {
		t.Fatalf("one unlisted: %s", snapshot.Text(workspace))
	}
	commit("local 6")
	if snapshot := Acquire(t.Context(), workspace); snapshot.Git.MoreUnpushed != 2 || !strings.HasSuffix(snapshot.Text(workspace), "(... 2 more unpushed commits)") {
		t.Fatalf("two unlisted: %s", snapshot.Text(workspace))
	}
	git(t, workspace, "update-ref", "refs/remotes/origin/main", "HEAD~3")
	if snapshot := Acquire(t.Context(), workspace); snapshot.Git.MoreUnpushed != 0 || strings.Contains(snapshot.Text(workspace), "unpushed") {
		t.Fatalf("all unpushed listed: %s", snapshot.Text(workspace))
	}
}

func TestSnapshotStatusHonorsGlobalExcludesFile(t *testing.T) {
	workspace := repository(t)
	for _, name := range []string{"ignored-globally", "visible"} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte("data"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	excludes := filepath.Join(t.TempDir(), "ignore")
	config := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(excludes, []byte("ignored-globally\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte("[core]\n excludesFile = "+excludes+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", config)
	snapshot := Acquire(t.Context(), workspace)
	if !slices.Equal(snapshot.Git.Status, []string{"?? visible"}) {
		t.Fatalf("status = %q", snapshot.Git.Status)
	}
}

func TestSnapshotsGitStatesAndIsolation(t *testing.T) {
	workspace := repository(t)
	unborn := Acquire(t.Context(), workspace)
	if unborn.Git.Branch != "topic" || unborn.Git.Head != "unborn" || unborn.Git.DefaultBranch != "unavailable" || len(unborn.Git.Recent) != 0 {
		t.Fatalf("unborn: %+v", unborn)
	}
	for n := range 6 {
		git(t, workspace, "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", fmt.Sprintf("subject %d \" </mekugi_environment_facts>", n))
	}
	head := git(t, workspace, "rev-parse", "HEAD")
	git(t, workspace, "update-ref", "refs/remotes/local/main", head)
	git(t, workspace, "symbolic-ref", "refs/remotes/local/HEAD", "refs/remotes/local/main")
	filename := "untracked\n\"file"
	if err := os.WriteFile(filepath.Join(workspace, filename), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	ordinary := Acquire(t.Context(), workspace)
	if !strings.HasPrefix(head, ordinary.Git.Head) || ordinary.Git.DefaultBranch != "main" || ordinary.Git.Linked || len(ordinary.Git.Recent) != 5 || len(ordinary.Git.Status) != 1 || !strings.Contains(ordinary.Git.Status[0], `\n`) {
		t.Fatalf("ordinary: %+v", ordinary)
	}
	encoded := ordinary.Text(workspace)
	if strings.Contains(encoded, "fixture@invalid") || strings.Contains(encoded, "remote_url") {
		t.Fatalf("identity leaked: %s", encoded)
	}
	linked := filepath.Join(t.TempDir(), "linked")
	git(t, workspace, "worktree", "add", "-q", "--detach", linked)
	detached := Acquire(t.Context(), linked)
	if !detached.Git.Linked || detached.Git.Branch != "detached" || detached.Git.Worktree != linked || !strings.HasPrefix(head, detached.Git.Head) {
		t.Fatalf("linked detached: %+v", detached)
	}
	git(t, workspace, "checkout", "--detach", "-q")
	if snapshot := Acquire(t.Context(), workspace); snapshot.Git.Branch != "detached" || snapshot.Git.Linked {
		t.Fatalf("ordinary detached: %+v", snapshot)
	}
}

func TestSnapshotHomeDirectory(t *testing.T) {
	userHome := filepath.Join(t.TempDir(), "user \"name\"\nnext line")
	for _, key := range []string{"HOME", "USERPROFILE", "home"} {
		t.Setenv(key, userHome)
	}
	for _, workspace := range []string{t.TempDir(), ""} {
		snapshot := Acquire(t.Context(), workspace)
		if snapshot.UserHome != userHome || !strings.Contains(snapshot.Text(workspace), "\nHome: "+strconv.Quote(userHome)+"\n") {
			t.Fatalf("workspace %q: home not preserved as quoted data: %+v", workspace, snapshot)
		}
	}
	for _, key := range []string{"HOME", "USERPROFILE", "home"} {
		t.Setenv(key, "")
	}
	userHome, err := os.UserHomeDir()
	snapshot := Acquire(t.Context(), "")
	if err != nil {
		if snapshot.UserHome != "" || !slices.Contains(snapshot.Incomplete, "user home unavailable") || !strings.Contains(snapshot.Text(""), "\nHome: unavailable\n") {
			t.Fatalf("unavailable home not explicit: %+v", snapshot)
		}
	} else if snapshot.UserHome != userHome {
		t.Fatalf("platform home default = %q, want %q", snapshot.UserHome, userHome)
	}
}

func TestSnapshotStatusAndSubjectBounds(t *testing.T) {
	workspace := repository(t)
	git(t, workspace, "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", strings.Repeat("界", 300))
	for n := range 40 {
		if err := os.WriteFile(filepath.Join(workspace, fmt.Sprintf("file-%02d", n)), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git(t, workspace, "add", "file-00")
	if err := os.WriteFile(filepath.Join(workspace, "file-00"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot := Acquire(t.Context(), workspace)
	if len(snapshot.Git.Status) != 32 || !strings.HasPrefix(snapshot.Git.Status[0], "AM ") || len(snapshot.Git.Recent[0].Subject) > 256 || len(snapshot.Incomplete) < 2 {
		t.Fatalf("bounds: %+v", snapshot)
	}
	var output boundedOutput
	output.Write([]byte(strings.Repeat("x", 20000)))
	if len(output.data) != 16384 || !output.truncated {
		t.Fatalf("output bound: %d %v", len(output.data), output.truncated)
	}
}

func TestSnapshotPlainDirectoryOptionalFailures(t *testing.T) {
	workspace := t.TempDir()
	for _, svn := range []bool{false, true} {
		if svn {
			if err := os.Mkdir(filepath.Join(workspace, ".svn"), 0700); err != nil {
				t.Fatal(err)
			}
		}
		snapshot := Acquire(t.Context(), workspace)
		text := snapshot.Text(workspace)
		if snapshot.Git.State != "none" || slices.Contains(snapshot.Incomplete, "repository unavailable") || !strings.Contains(text, "Git repository: no") || strings.Contains(text, "Status:") {
			t.Fatalf("svn=%t: %s", svn, text)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	unavailable := Acquire(ctx, workspace)
	if unavailable.OSRelease != "unavailable" || unavailable.Git.State != "unavailable" || len(unavailable.Incomplete) != 2 {
		t.Fatalf("failure: %+v", unavailable)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".git"), []byte("invalid git metadata"), 0600); err != nil {
		t.Fatal(err)
	}
	broken := Acquire(t.Context(), workspace).Text(workspace)
	if !strings.Contains(broken, "Git repository: unavailable") || !strings.Contains(broken, "Incomplete: repository unavailable") {
		t.Fatalf("corruption presented as absence: %s", broken)
	}
	t.Setenv("PATH", t.TempDir())
	missing := Acquire(t.Context(), workspace)
	if missing.Git.State != "unavailable" || !slices.Contains(missing.Incomplete, "repository unavailable") {
		t.Fatalf("missing Git presented as absence: %+v", missing)
	}
}

func TestSnapshotSubmoduleAndMixedSVN(t *testing.T) {
	source := repository(t)
	git(t, source, "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "submodule")
	workspace := repository(t)
	git(t, workspace, "-c", "protocol.file.allow=always", "submodule", "add", "-q", source, "sub")
	git(t, workspace, "-c", "commit.gpgsign=false", "commit", "-qam", "parent")
	if err := os.Mkdir(filepath.Join(workspace, ".svn"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "sub", "dirty"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot := Acquire(t.Context(), workspace)
	if snapshot.Git.State != "repository" || len(snapshot.Git.Status) != 1 || !strings.Contains(snapshot.Git.Status[0], "sub") {
		t.Fatalf("mixed/submodule: %+v", snapshot)
	}
	child := Acquire(t.Context(), filepath.Join(workspace, "sub"))
	if len(child.Git.Recent) != 1 || child.Git.Recent[0].Subject != "submodule" {
		t.Fatalf("submodule head: %+v", child)
	}
}

func TestSnapshotNestedRemoteDefaultBranch(t *testing.T) {
	workspace := repository(t)
	git(t, workspace, "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "base")
	head := git(t, workspace, "rev-parse", "HEAD")
	git(t, workspace, "update-ref", "refs/remotes/company/team/release/next", head)
	git(t, workspace, "symbolic-ref", "refs/remotes/company/team/HEAD", "refs/remotes/company/team/release/next")
	if snapshot := Acquire(t.Context(), workspace); snapshot.Git.DefaultBranch != "release/next" {
		t.Fatalf("nested default branch: %+v", snapshot)
	}
	git(t, workspace, "update-ref", "refs/remotes/other/main", head)
	git(t, workspace, "symbolic-ref", "refs/remotes/other/HEAD", "refs/remotes/other/main")
	if snapshot := Acquire(t.Context(), workspace); snapshot.Git.DefaultBranch != "unavailable" {
		t.Fatalf("conflicting defaults guessed: %+v", snapshot)
	}
}

func TestSnapshotPartialCloneNeverFetches(t *testing.T) {
	donor := repository(t)
	if err := os.Mkdir(filepath.Join(donor, "tree"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(donor, "tree", "file"), []byte("body"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, donor, "add", "-A")
	git(t, donor, "-c", "commit.gpgsign=false", "commit", "-qm", "partial source")
	git(t, donor, "config", "uploadpack.allowFilter", "true")
	client := filepath.Join(t.TempDir(), "partial")
	git(t, donor, "-c", "protocol.file.allow=always", "clone", "-q", "--filter=tree:0", "--no-checkout", "file://"+filepath.ToSlash(donor), client)
	if _, _, err := command(t.Context(), client, "git", "--no-lazy-fetch", "cat-file", "-e", "HEAD^{tree}"); err == nil {
		t.Fatal("fixture has no missing promised tree")
	}
	packs := func() string {
		t.Helper()
		entries, err := os.ReadDir(filepath.Join(client, ".git", "objects", "pack"))
		if err != nil {
			t.Fatal(err)
		}
		var state strings.Builder
		for _, entry := range entries {
			info, err := entry.Info()
			if err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&state, "%s:%d\n", entry.Name(), info.Size())
		}
		return state.String()
	}
	before := packs()
	snapshot := Acquire(t.Context(), client)
	if after := packs(); after != before {
		t.Fatal("snapshot lazily fetched or modified promised objects")
	}
	if !slices.Contains(snapshot.Incomplete, "status unavailable") {
		t.Fatalf("missing promised tree did not remain unavailable: %+v", snapshot)
	}
}

func TestSnapshotCompactText(t *testing.T) {
	workspace := repository(t)
	git(t, workspace, "config", "core.abbrev", "false")
	git(t, workspace, "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "base")
	snapshot := Acquire(t.Context(), workspace)
	t.Logf("Host OS: %s", snapshot.OSName)
	text := snapshot.Text(workspace)
	if !strings.Contains(text, "Git repository: yes") {
		t.Fatalf("Git membership missing: %s", text)
	}
	for _, duplicate := range []string{workspace, "Incomplete:", "repository_root", git(t, workspace, "rev-parse", "HEAD")} {
		if strings.Contains(text, duplicate) {
			t.Fatalf("redundant %q in %s", duplicate, text)
		}
	}
	if len(snapshot.Git.Recent[0].ID) >= 40 || !strings.HasPrefix(git(t, workspace, "rev-parse", "HEAD"), snapshot.Git.Recent[0].ID) {
		t.Fatalf("commit not abbreviated: %+v", snapshot.Git.Recent)
	}
	subdir := filepath.Join(workspace, "nested")
	if err := os.Mkdir(subdir, 0700); err != nil {
		t.Fatal(err)
	}
	if text := Acquire(t.Context(), subdir).Text(subdir); !strings.Contains(text, "Git worktree: "+strconv.Quote(workspace)) {
		t.Fatalf("distinct Git worktree missing: %s", text)
	}
	snapshot.Incomplete = []string{"status unavailable"}
	snapshot.Git.Status = nil
	if text := snapshot.Text(workspace); !strings.Contains(text, "Incomplete: status unavailable") || strings.Contains(text, "clean") {
		t.Fatalf("failed status presented as clean: %s", text)
	}
}

func TestReleaseName(t *testing.T) {
	for _, data := range []string{`NAME=Debian
PRETTY_NAME="Debian GNU/Linux 13 (trixie)"`, "PRETTY_NAME='Debian GNU/Linux 13 (trixie)'", "PRETTY_NAME=Debian GNU/Linux 13 (trixie)"} {
		if got := releaseName(data); got != "Debian GNU/Linux 13 (trixie)" {
			t.Fatalf("release name: %q", got)
		}
	}
	if got := releaseName("ID=debian"); got != "" {
		t.Fatalf("missing identity: %q", got)
	}
}
