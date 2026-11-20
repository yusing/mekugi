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
	// Read through a testdata symlink so Go's test cache keys on it.
	gitignore, err := os.ReadFile("testdata/gitignore")
	if err != nil {
		t.Fatal(err)
	}
	ignoreFiles := map[string]string{".gitignore": string(gitignore)}
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
	for path, content := range ignoreFiles {
		writeWorkspaceSource(t, path, content)
	}
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

type nativeIntegrationFixture struct {
	integrationFixture
	parent, nativeBase, oldPin, incomingPin string
}

func newNativeIntegrationFixture(t *testing.T, divergent bool) nativeIntegrationFixture {
	t.Helper()
	f := nativeIntegrationFixture{integrationFixture: newIntegrationFixture(t)}
	f.git(t, "init", "-b", "main", integrationNativePath)
	native := func(args ...string) string {
		t.Helper()
		return f.git(t, append([]string{"-C", integrationNativePath}, args...)...)
	}
	native("commit", "--allow-empty", "-m", "native base")
	f.nativeBase = native("rev-parse", "HEAD")
	native("switch", "-c", "reviewed")
	writeWorkspaceSource(t, integrationNativePath+"/reviewed", "native reviewed\n")
	native("add", "reviewed")
	native("commit", "-m", "native reviewed")
	f.incomingPin = native("rev-parse", "HEAD")
	native("switch", "main")
	if divergent {
		writeWorkspaceSource(t, integrationNativePath+"/neighbor", "native neighbor\n")
		native("add", "neighbor")
		native("commit", "-m", "native neighbor")
	}
	f.oldPin = native("rev-parse", "HEAD")
	writeWorkspaceSource(t, ".gitmodules", "[submodule \"runtime/codex\"]\n\tpath = runtime/codex\n\turl = ../codex\n")
	f.git(t, "add", ".gitmodules")
	f.git(t, "update-index", "--add", "--cacheinfo", "160000,"+f.oldPin+","+integrationNativePath)
	f.git(t, "commit", "-m", "pin native")
	f.parent = f.git(t, "rev-parse", "HEAD")
	f.git(t, "-C", f.source, "switch", "-C", "task/reviewed", f.parent)
	f.review(t, f.incomingPin)
	return f
}

func (f *nativeIntegrationFixture) review(t *testing.T, pin string) {
	t.Helper()
	f.git(t, "-C", f.source, "update-index", "--cacheinfo", "160000,"+pin+","+integrationNativePath)
	writeWorkspaceSource(t, filepath.Join(f.source, "tracked"), "reviewed\n")
	f.git(t, "-C", f.source, "add", "tracked")
	f.git(t, "-C", f.source, "commit", "-m", "reviewed pair")
	f.commit = f.git(t, "-C", f.source, "rev-parse", "HEAD")
}

func TestIntegrationMergeKeepsBothHistoriesAndChecksCommittedPin(t *testing.T) {
	f := newNativeIntegrationFixture(t, true)
	writeWorkspaceSource(t, "go.work.sum", "unrelated edit\n")
	writeWorkspaceSource(t, "untracked", "unrelated file\n")
	check := `test "$(git rev-parse HEAD:runtime/codex)" = "$(git -C runtime/codex rev-parse HEAD)" && check pair`
	if err := integrate([]string{"--merge", f.commit, "--", "sh", "-ec", check}); err != nil {
		t.Fatal(err)
	}
	if parents := f.git(t, "show", "-s", "--format=%P", "HEAD"); parents != f.parent+" "+f.commit {
		t.Fatalf("Mekugi parents: %s", parents)
	}
	if parents := f.git(t, "-C", integrationNativePath, "show", "-s", "--format=%P", "HEAD"); parents != f.oldPin+" "+f.incomingPin {
		t.Fatalf("native parents: %s", parents)
	}
	for path, want := range map[string]string{"go.work.sum": "unrelated edit\n", "untracked": "unrelated file\n", f.events: "check pair\n"} {
		if data, err := os.ReadFile(path); err != nil || string(data) != want {
			t.Fatalf("%s: got %q, %v, want %q", path, data, err, want)
		}
	}
}

func TestIntegrationMergeResolvesDivergentGitlinks(t *testing.T) {
	f := newNativeIntegrationFixture(t, true)
	f.git(t, "-C", integrationNativePath, "commit", "--allow-empty", "-m", "advance native main")
	nativeBefore := f.git(t, "-C", integrationNativePath, "rev-parse", "HEAD")
	f.git(t, "update-index", "--cacheinfo", "160000,"+nativeBefore+","+integrationNativePath)
	f.git(t, "commit", "-m", "advance Mekugi pin")
	if err := integrate([]string{"--merge", f.commit}); err != nil {
		t.Fatal(err)
	}
	if parents := f.git(t, "-C", integrationNativePath, "show", "-s", "--format=%P", "HEAD"); parents != nativeBefore+" "+f.incomingPin {
		t.Fatalf("native parents: %s", parents)
	}
	if pin := f.git(t, "rev-parse", "HEAD:"+integrationNativePath); pin != f.git(t, "-C", integrationNativePath, "rev-parse", "HEAD") {
		t.Fatalf("committed gitlink is not the native merge result: %s", pin)
	}
}

func TestIntegrationMergeIgnoresUnchangedIncomingOldPin(t *testing.T) {
	f := newNativeIntegrationFixture(t, true)
	f.git(t, "-C", f.source, "switch", "-C", "task/go-only", f.parent)
	writeWorkspaceSource(t, filepath.Join(f.source, "go-only"), "reviewed\n")
	f.git(t, "-C", f.source, "add", "go-only")
	f.git(t, "-C", f.source, "commit", "-m", "Go-only change")
	commit := f.git(t, "-C", f.source, "rev-parse", "HEAD")
	// Advance the parent gitlink without checking out that native commit.
	f.git(t, "update-index", "--cacheinfo", "160000,"+f.incomingPin+","+integrationNativePath)
	f.git(t, "commit", "-m", "separate native update")
	if err := integrate([]string{"--merge", commit, "--", "check", "go-only"}); err != nil {
		t.Fatal(err)
	}
	if pin := f.git(t, "rev-parse", "HEAD:"+integrationNativePath); pin != f.incomingPin {
		t.Fatalf("Go-only merge changed the destination pin to %s", pin)
	}
	if native := f.git(t, "-C", integrationNativePath, "rev-parse", "HEAD"); native != f.oldPin {
		t.Fatalf("Go-only merge moved the native checkout to %s", native)
	}
}

func TestIntegrationMergeChecksPinChangedOnBothBranches(t *testing.T) {
	f := newNativeIntegrationFixture(t, false)
	f.git(t, "update-index", "--cacheinfo", "160000,"+f.incomingPin+","+integrationNativePath)
	f.git(t, "commit", "-m", "independently pin the same native change")
	head := f.git(t, "rev-parse", "HEAD")
	// Both reviewed tips pin the new commit, but the checkout is still at the base pin.
	if err := integrate([]string{"--merge", f.commit, "--", "check"}); err == nil {
		t.Fatal("paired merge accepted the stale native checkout")
	}
	if f.git(t, "rev-parse", "HEAD") != head {
		t.Fatal("Mekugi advanced after preflight rejection")
	}
	if _, err := os.Stat(f.events); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("check ran after preflight rejection: %v", err)
	}
}

func TestIntegrationMergePreflight(t *testing.T) {
	for _, scenario := range []string{"dirty native", "detached native", "native branch", "Mekugi branch", "wrong native pin", "missing incoming", "rewind", "busy Mekugi", "busy native", "ignored Mekugi collision", "ignored native collision"} {
		t.Run(scenario, func(t *testing.T) {
			f := newNativeIntegrationFixture(t, true)
			var protected string
			switch scenario {
			case "dirty native":
				writeWorkspaceSource(t, integrationNativePath+"/local", "keep\n")
			case "detached native":
				f.git(t, "-C", integrationNativePath, "switch", "--detach", f.oldPin)
			case "native branch":
				f.git(t, "-C", integrationNativePath, "switch", "-c", "wrong")
			case "Mekugi branch":
				f.git(t, "switch", "-c", "wrong")
			case "wrong native pin":
				f.git(t, "-C", integrationNativePath, "reset", "--keep", f.nativeBase)
			case "missing incoming":
				f.review(t, strings.Repeat("1", len(f.incomingPin)))
			case "rewind":
				f.review(t, f.nativeBase)
			case "busy Mekugi":
				writeWorkspaceSource(t, f.git(t, "rev-parse", "--git-path", "MERGE_HEAD"), f.commit+"\n")
			case "busy native":
				path := f.git(t, "-C", integrationNativePath, "rev-parse", "--path-format=absolute", "--git-path", "MERGE_HEAD")
				writeWorkspaceSource(t, path, f.incomingPin+"\n")
			case "ignored Mekugi collision":
				writeWorkspaceSource(t, filepath.Join(f.source, "private"), "incoming\n")
				f.git(t, "-C", f.source, "add", "-f", "private")
				f.git(t, "-C", f.source, "commit", "-m", "collision")
				f.commit = f.git(t, "-C", f.source, "rev-parse", "HEAD")
				protected = "private"
				writeWorkspaceSource(t, protected, "keep\n")
			case "ignored native collision":
				writeWorkspaceSource(t, integrationNativePath+"/.git/info/exclude", "/reviewed\n")
				protected = integrationNativePath + "/reviewed/local"
				writeWorkspaceSource(t, protected, "keep\n")
			}
			native := f.git(t, "-C", integrationNativePath, "rev-parse", "HEAD")
			if err := integrate([]string{"--merge", f.commit, "--", "check"}); err == nil {
				t.Fatal("unsafe paired merge succeeded")
			}
			if f.git(t, "rev-parse", "HEAD") != f.parent || f.git(t, "-C", integrationNativePath, "rev-parse", "HEAD") != native {
				t.Fatal("a repository advanced after preflight rejection")
			}
			if _, err := os.Stat(f.events); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("check ran after preflight rejection: %v", err)
			}
			if protected != "" {
				if data, err := os.ReadFile(protected); err != nil || string(data) != "keep\n" {
					t.Fatalf("ignored local file changed: %q, %v", data, err)
				}
			}
		})
	}
}

func TestIntegrationMergeRetainsPartialState(t *testing.T) {
	for _, scenario := range []string{"conflict", "commit hook"} {
		t.Run(scenario, func(t *testing.T) {
			f := newNativeIntegrationFixture(t, false)
			switch scenario {
			case "conflict":
				writeWorkspaceSource(t, "tracked", "Mekugi conflict\n")
				f.git(t, "commit", "-am", "conflict")
			case "commit hook":
				hook := f.git(t, "rev-parse", "--git-path", "hooks/pre-commit")
				writeWorkspaceSource(t, hook, "#!/bin/sh\nexit 1\n")
				if err := os.Chmod(hook, 0700); err != nil {
					t.Fatal(err)
				}
			}
			head := f.git(t, "rev-parse", "HEAD")
			if err := integrate([]string{"--merge", f.commit, "--accept", "--items", "MEKUGI-1", "--state", "Done", "--cleanup", f.source, "--", "check"}); err == nil {
				t.Fatal("failed parent merge reported success")
			}
			if f.git(t, "rev-parse", "HEAD") != head || f.git(t, "-C", integrationNativePath, "rev-parse", "HEAD") != f.incomingPin {
				t.Fatal("partial result was lost or parent committed")
			}
			if _, err := os.Stat(f.events); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("checks or Plane ran after failure: %v", err)
			}
			if _, err := os.Stat(f.source); err != nil {
				t.Fatalf("source worktree removed: %v", err)
			}
			if f.git(t, "rev-parse", "MERGE_HEAD") != f.commit {
				t.Fatal("Mekugi merge state not retained")
			}
			if scenario == "conflict" && f.git(t, "diff", "--name-only", "--diff-filter=U") != "tracked" {
				t.Fatal("Mekugi conflict not retained")
			}
		})
	}
}

func TestIntegrationNativeChecksUseTheFinalPin(t *testing.T) {
	for _, scenario := range []string{"prepared pick", "mismatched pick", "moves during check", "checked-head moved", "checked-head matches"} {
		t.Run(scenario, func(t *testing.T) {
			f := newNativeIntegrationFixture(t, false)
			args := []string{"--commits", f.commit, "--", "check"}
			switch scenario {
			case "prepared pick":
				f.git(t, "-C", integrationNativePath, "merge", "--ff-only", f.incomingPin)
			case "moves during check":
				args = []string{"--merge", f.commit, "--accept", "--items", "MEKUGI-1", "--state", "Done", "--",
					"git", "-C", integrationNativePath, "switch", "--detach", f.oldPin}
			case "checked-head moved", "checked-head matches":
				if err := integrate([]string{"--merge", f.commit}); err != nil {
					t.Fatal(err)
				}
				if scenario == "checked-head moved" {
					f.git(t, "-C", integrationNativePath, "switch", "--detach", f.oldPin)
				}
				args = []string{"--checked-head", f.git(t, "rev-parse", "HEAD"), "--verify-native", "--accept", "--items", "MEKUGI-1", "--state", "Done"}
			}
			ok := scenario == "prepared pick" || scenario == "checked-head matches"
			if err := integrate(args); (err == nil) != ok {
				t.Fatalf("expected success=%t, got %v", ok, err)
			}
			if !ok {
				if _, err := os.Stat(f.events); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("unexpected check or finalization: %v", err)
				}
			}
			if pin := f.git(t, "rev-parse", "HEAD:"+integrationNativePath); pin != f.incomingPin {
				t.Fatalf("expected the committed pin to remain %s, got %s", f.incomingPin, pin)
			}
		})
	}
}

func TestIntegrationDirtyRootChecksAndStatusChoices(t *testing.T) {
	f := newIntegrationFixture(t)
	// Use the same ignored destinations as make webui.
	for _, path := range []string{
		"apps/mekugi/internal/webui/app/node_modules/.bun-install",
		"apps/mekugi/internal/webui/dist/app/assets/app.js",
	} {
		writeWorkspaceSource(t, filepath.Join(f.source, path), "generated\n")
	}
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

func TestIntegrationProtectsGeneratedOutputNeighbors(t *testing.T) {
	for _, path := range []string{
		"apps/mekugi/internal/webui/app/.env.local",
		"apps/mekugi/internal/webui/dist/.env",
		"apps/mekugi/internal/webui/dist/app-local/.env",
		"other/node_modules/source.js",
		"bin/mekugi",
		"apps/mekugi/internal/webui/app/.env.\nconfiguration",
	} {
		t.Run(path, func(t *testing.T) {
			f := newIntegrationFixture(t)
			writeWorkspaceSource(t, filepath.Join(f.source, "apps/mekugi/internal/webui/dist/app/assets/app.js"), "generated\n")
			writeWorkspaceSource(t, filepath.Join(f.source, path), "keep\n")
			if err := integrate([]string{"--commits", f.commit, "--accept", "--cleanup", f.source, "--", "check"}); err == nil {
				t.Fatal("cleanup discarded protected source beside generated output")
			}
			for _, retained := range []string{path, "apps/mekugi/internal/webui/dist/app/assets/app.js"} {
				want := "keep\n"
				if retained != path {
					want = "generated\n"
				}
				data, err := os.ReadFile(filepath.Join(f.source, retained))
				if err != nil || string(data) != want {
					t.Fatalf("discarded %s after refusal: %v", retained, err)
				}
			}
			if f.git(t, "branch", "--list", "task/reviewed") == "" {
				t.Fatal("source branch removed after refusal")
			}
		})
	}
}

func TestIntegrationRefusalsAndFailurePropagation(t *testing.T) {
	for _, scenario := range []string{"staged", "tracked overlap", "committed conflict", "untracked overlap", "signing", "failed check", "missing command", "missing check", "missing acceptance", "stale check", "partial status", "dirty source", "ignored source", "tracked generated source", "ignored output parent", "unintegrated", "merge-only", "wrong target", "source subdirectory", "foreign source", "locked source"} {
		t.Run(scenario, func(t *testing.T) {
			f := newIntegrationFixture(t)
			generated := "apps/mekugi/internal/webui/dist/app/assets/app.js"
			writeWorkspaceSource(t, filepath.Join(f.source, generated), "generated\n")
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
			case "tracked generated source":
				f.git(t, "-C", f.source, "add", "-f", generated)
				f.git(t, "-C", f.source, "commit", "-m", "tracked output")
				args[1] += " " + f.git(t, "-C", f.source, "rev-parse", "HEAD")
				writeWorkspaceSource(t, filepath.Join(f.source, generated), "keep\n")
			case "ignored output parent":
				writeWorkspaceSource(t, ".git/info/exclude", ".worktrees/\napps/mekugi/internal/webui/dist/\n")
				writeWorkspaceSource(t, filepath.Join(f.source, "apps/mekugi/internal/webui/dist/source.txt"), "keep\n")
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
			if _, err := os.Stat(filepath.Join(f.source, generated)); err != nil {
				t.Fatalf("generated output removed before cleanup guards passed: %v", err)
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
	makefile := repositoryMakefile(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f := newIntegrationFixture(t)
	writeWorkspaceSource(t, filepath.Join(f.source, "apps/mekugi/internal/webui/app/node_modules/.bun-install"), "generated\n")
	writeWorkspaceSource(t, filepath.Join(f.source, "apps/mekugi/internal/webui/dist/app/assets/app.js"), "generated\n")
	writeWorkspaceSource(t, "Makefile", string(makefile))
	wrapper := filepath.Join(t.TempDir(), "workspace")
	writeWorkspaceSource(t, wrapper, "#!/bin/sh\nexec '"+strings.ReplaceAll(executable, "'", "'\\''")+"' -test.run=^TestMakefileWorkspaceFixture$ -- \"$@\"\n")
	if err := os.Chmod(wrapper, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MEKUGI_MAKEFILE_WORKSPACE_FIXTURE", "1")
	command := exec.Command("make", "--no-print-directory", "integrate", "WORKSPACE="+wrapper, "INTEGRATE_ARGS=--commits "+f.commit+" --accept --cleanup '"+f.source+"' -- check focused")
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
	if _, err := os.Stat(f.source); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("make integrate retained generated worktree: %v", err)
	}
	if f.git(t, "branch", "--list", "task/reviewed") != "" {
		t.Fatal("make integrate retained source branch")
	}
}
