package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type integrationFixture struct{ root, source, commit, events string }

func newIntegrationFixture(t *testing.T) integrationFixture {
	t.Helper()
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "GIT_") {
			t.Setenv(name, "")
			if err := os.Unsetenv(name); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, name := range []string{"GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM"} {
		t.Setenv(name, os.DevNull)
	}
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, role := range []string{"AUTHOR", "COMMITTER"} {
		t.Setenv("GIT_"+role+"_NAME", "Fixture")
		t.Setenv("GIT_"+role+"_EMAIL", "fixture@example.invalid")
	}
	directory := t.TempDir()
	f := integrationFixture{root: filepath.Join(directory, "repository"), events: filepath.Join(directory, "events")}
	f.source = filepath.Join(f.root, ".worktrees", "reviewed work")
	if err := os.MkdirAll(f.root, 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(f.root)
	f.git(t, "init", "-b", "main")
	f.git(t, "config", "commit.gpgsign", "false")
	writeWorkspaceSource(t, ".git/info/exclude", ".worktrees/\nprivate\n")
	for _, path := range []string{"tracked", "go.work.sum", "docs/design.md"} {
		writeWorkspaceSource(t, path, "base\n")
	}
	f.git(t, "add", ".")
	f.git(t, "commit", "-m", "base")
	f.git(t, "worktree", "add", "-b", "task/reviewed", f.source)
	writeWorkspaceSource(t, filepath.Join(f.source, "tracked"), "reviewed\n")
	f.git(t, "-C", f.source, "commit", "-am", "reviewed")
	f.commit = f.git(t, "-C", f.source, "rev-parse", "HEAD")
	f.git(t, "commit", "--allow-empty", "-m", "neighbor")
	for name, script := range map[string]string{
		"check": "printf 'check %s\\n' \"$*\" >> \"$INTEGRATION_EVENTS\"\nexit \"${CHECK_EXIT:-0}\"\n",
		"plane": "printf 'plane %s\\n' \"$*\" >> \"$INTEGRATION_EVENTS\"\n[ \"$2\" != \"${PLANE_FAIL_ITEM:-}\" ] || exit 9\n",
	} {
		path := filepath.Join(directory, "bin", name)
		writeWorkspaceSource(t, path, "#!/bin/sh\n"+script)
		if err := os.Chmod(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", filepath.Join(directory, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("INTEGRATION_EVENTS", f.events)
	return f
}

func (f integrationFixture) git(t *testing.T, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	data, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, data)
	}
	return strings.TrimSpace(string(data))
}

func TestIntegrationDirtyRootChecksAndStatusChoices(t *testing.T) {
	f := newIntegrationFixture(t)
	for _, path := range []string{"go.work.sum", "docs/design.md", "untracked"} {
		writeWorkspaceSource(t, path, "unrelated work\n")
	}
	before := f.git(t, "diff", "HEAD")
	writeWorkspaceSource(t, filepath.Join(f.source, "second"), "second\n")
	f.git(t, "-C", f.source, "add", "second")
	f.git(t, "-C", f.source, "commit", "-m", "second")
	second := f.git(t, "-C", f.source, "rev-parse", "HEAD")
	if err := integrate([]string{"--commits", f.commit + " " + second, "--accept", "--items", "MEKUGI-1 MEKUGI-2", "--state", "Pending QA", "--", "check", "focus exactly"}); err != nil {
		t.Fatal(err)
	}
	head := f.git(t, "rev-parse", "HEAD")
	if head == second {
		t.Fatal("fixture must exercise patch equivalence rather than ancestry")
	}
	if err := integrate([]string{"--accept", "--items", "MEKUGI-1 MEKUGI-2", "--state", "Done", "--checked-head", head, "--cleanup", f.source}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(f.events)
	if err != nil {
		t.Fatal(err)
	}
	want := "check focus exactly\nplane update MEKUGI-1 --state Pending QA\nplane update MEKUGI-2 --state Pending QA\nplane update MEKUGI-1 --state Done\nplane update MEKUGI-2 --state Done\n"
	if string(data) != want {
		t.Fatalf("two calls must reuse one focused check and preserve chosen statuses: %s", data)
	}
	if f.git(t, "diff", "HEAD") != before {
		t.Fatal("unrelated tracked dirt changed")
	}
	for _, path := range []string{"go.work.sum", "docs/design.md", "untracked"} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "unrelated work\n" {
			t.Fatalf("discarded %s: %v", path, err)
		}
	}
	if _, err := os.Stat(f.source); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree retained: %v", err)
	}
	if output := f.git(t, "branch", "--list", "task/reviewed"); output != "" {
		t.Fatal("authorized branch cleanup did not run")
	}
}

func TestIntegrationRefusalsAndFailurePropagation(t *testing.T) {
	for _, scenario := range []string{"staged", "tracked overlap", "committed conflict", "untracked overlap", "signing", "failed check", "missing command", "missing check", "missing acceptance", "stale check", "partial status", "dirty source", "ignored source", "unintegrated", "merge-only", "wrong target", "source subdirectory", "foreign source", "locked source"} {
		t.Run(scenario, func(t *testing.T) {
			f := newIntegrationFixture(t)
			args := []string{"--commits", f.commit, "--accept", "--items", "MEKUGI-1 MEKUGI-2", "--state", "Done", "--cleanup", f.source, "--", "check"}
			switch scenario {
			case "staged":
				writeWorkspaceSource(t, "staged", "keep\n")
				f.git(t, "add", "staged")
			case "tracked overlap":
				writeWorkspaceSource(t, "tracked", "keep\n")
			case "committed conflict":
				writeWorkspaceSource(t, "tracked", "conflict\n")
				f.git(t, "commit", "-am", "conflict")
			case "untracked overlap":
				writeWorkspaceSource(t, filepath.Join(f.source, "collision"), "source\n")
				f.git(t, "-C", f.source, "add", "collision")
				f.git(t, "-C", f.source, "commit", "-m", "collision")
				args[1] += " " + f.git(t, "-C", f.source, "rev-parse", "HEAD")
				writeWorkspaceSource(t, "collision", "keep\n")
			case "signing":
				f.git(t, "config", "commit.gpgsign", "true")
				f.git(t, "config", "gpg.program", "/bin/false")
			case "failed check":
				t.Setenv("CHECK_EXIT", "7")
			case "missing command":
				args[10] = filepath.Join(t.TempDir(), "missing-check")
			case "missing check":
				args = args[:10]
			case "missing acceptance":
				args = append(args[:2], args[3:]...)
			case "stale check":
				args = []string{"--accept", "--items", "MEKUGI-1", "--state", "Done", "--checked-head", f.commit}
			case "partial status":
				t.Setenv("PLANE_FAIL_ITEM", "MEKUGI-2")
			case "dirty source", "ignored source":
				path := "untracked"
				if scenario == "ignored source" {
					path = "private"
				}
				writeWorkspaceSource(t, filepath.Join(f.source, path), "keep\n")
			case "unintegrated":
				writeWorkspaceSource(t, filepath.Join(f.source, "extra"), "keep\n")
				f.git(t, "-C", f.source, "add", "extra")
				f.git(t, "-C", f.source, "commit", "-m", "extra")
			case "merge-only":
				f.git(t, "-C", f.source, "merge", "--no-ff", "--no-commit", "main")
				writeWorkspaceSource(t, filepath.Join(f.source, "merge-only"), "keep\n")
				f.git(t, "-C", f.source, "add", "merge-only")
				f.git(t, "-C", f.source, "commit", "-m", "merge-only")
			case "wrong target":
				args[8] = f.root
			case "source subdirectory":
				args[8] = filepath.Join(f.source, "subdirectory")
				if err := os.Mkdir(args[8], 0700); err != nil {
					t.Fatal(err)
				}
			case "foreign source":
				args[8] = filepath.Join(f.root, ".worktrees", "foreign")
				f.git(t, "init", args[8])
			case "locked source":
				f.git(t, "worktree", "lock", f.source)
			}
			staged := f.git(t, "diff", "--cached")
			err := integrate(args)
			if err == nil {
				t.Fatal("unsafe or failed sequence succeeded")
			}
			if scenario == "failed check" || scenario == "partial status" {
				want := 7
				if scenario == "partial status" {
					want = 9
				}
				if status, ok := errors.AsType[*exec.ExitError](err); !ok || status.ExitCode() != want {
					t.Fatalf("lost failure status: %v", err)
				}
			}
			if _, err := os.Stat(f.source); err != nil {
				t.Fatalf("source removed after failure: %v", err)
			}
			if scenario == "staged" && f.git(t, "diff", "--cached") != staged {
				t.Fatal("pre-existing index changed")
			}
			data, _ := os.ReadFile(f.events)
			switch scenario {
			case "staged", "tracked overlap", "committed conflict", "untracked overlap", "signing", "failed check", "missing command", "missing check", "missing acceptance", "stale check":
				if strings.Contains(string(data), "plane ") {
					t.Fatalf("status after failed/missing checks or authority: %s", data)
				}
			}
			if scenario == "committed conflict" && !strings.Contains(f.git(t, "status", "--porcelain"), "UU tracked") {
				t.Fatal("conflict state was discarded")
			}
			if scenario == "merge-only" && strings.Contains(f.git(t, "cherry", "HEAD", "task/reviewed"), "+") {
				t.Fatal("merge fixture must expose work git cherry omits")
			}
		})
	}
}

func TestIntegrationMakeRecipe(t *testing.T) {
	makefile, err := os.ReadFile("../../../../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f := newIntegrationFixture(t)
	writeWorkspaceSource(t, "Makefile", string(makefile))
	wrapper := filepath.Join(t.TempDir(), "workspace")
	writeWorkspaceSource(t, wrapper, "#!/bin/sh\nexec '"+strings.ReplaceAll(executable, "'", "'\\''")+"' -test.run=^TestMakefileWorkspaceFixture$ -- \"$@\"\n")
	if err := os.Chmod(wrapper, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MEKUGI_MAKEFILE_WORKSPACE_FIXTURE", "1")
	command := exec.Command("make", "--no-print-directory", "integrate", "WORKSPACE="+wrapper, "INTEGRATE_ARGS=--commits "+f.commit+" -- check focused")
	data, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("make integrate: %v\n%s", err, data)
	}
	if !strings.Contains(string(data), "Integration HEAD:") {
		t.Fatalf("missing result: %s", data)
	}
	if data, err := os.ReadFile(f.events); err != nil || string(data) != "check focused\n" {
		t.Fatalf("make did not route supplied check: %s, %v", data, err)
	}
}
