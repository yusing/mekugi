package router

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi"
)

func TestGitCleanProviderPreservesExclusions(t *testing.T) {
	for _, option := range []string{"-e keep", "-ekeep", "--exclude keep", "--exclude=keep"} {
		t.Run(option, func(t *testing.T) {
			repo := newExecVCSTestRepo(t, map[string]string{"tracked": "base"})
			writeTestFile(t, filepath.Join(repo, "keep"), "keep")
			writeTestFile(t, filepath.Join(repo, "discard"), "discard")
			observation := captureExecVCSTestCommand(t, repo, "git clean -fd "+option)
			assertCapturedExecVCSTestFile(t, observation, filepath.Join(repo, "discard"), "discard")
			for _, file := range observation.Files {
				if file.Path == filepath.Join(repo, "keep") {
					t.Fatal("excluded path captured as a deletion target")
				}
			}
		})
	}
}

func TestGitDiscardProvidersCaptureExactBeforeBytes(t *testing.T) {
	for _, test := range []struct {
		name, command string
	}{
		{name: "restore path", command: "git restore -- tracked.txt"},
		{name: "reset hard", command: "git reset --hard"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := newExecVCSTestRepo(t, map[string]string{"tracked.txt": "committed bytes\n"})
			writeTestFile(t, filepath.Join(repo, "tracked.txt"), "worktree bytes\n")
			observation := captureExecVCSTestCommand(t, repo, test.command)
			assertCapturedExecVCSTestFile(t, observation, filepath.Join(repo, "tracked.txt"), "worktree bytes\n")
			assertExecVCSTestFileContent(t, filepath.Join(repo, "tracked.txt"), "worktree bytes\n")

			runExecVCSTestGit(t, repo, strings.Fields(test.command)[1:]...)
			reviews := reconcileExecVCSTestExact(t, observation)
			if len(reviews) != 1 || reviews[0].Action().Title() != "Edit" ||
				!strings.Contains(reviews[0].Diff, "-worktree bytes") || !strings.Contains(reviews[0].Diff, "+committed bytes") {
				t.Fatalf("%s evidence = %+v; want the discarded worktree bytes and committed bytes", test.name, reviews)
			}
		})
	}
}

func TestGitResetHardCapturesStagedContentAgainstHEAD(t *testing.T) {
	repo := newExecVCSTestRepo(t, map[string]string{"tracked.txt": "committed bytes\n"})
	writeTestFile(t, filepath.Join(repo, "tracked.txt"), "staged bytes\n")
	runExecVCSTestGit(t, repo, "add", "--", "tracked.txt")
	assertExecVCSTestFileContent(t, filepath.Join(repo, "tracked.txt"), "staged bytes\n")

	observation := captureExecVCSTestCommand(t, repo, "git reset --hard")
	assertCapturedExecVCSTestFile(t, observation, filepath.Join(repo, "tracked.txt"), "staged bytes\n")
	assertExecVCSTestFileContent(t, filepath.Join(repo, "tracked.txt"), "staged bytes\n")
	runExecVCSTestGit(t, repo, "reset", "--hard")

	reviews := reconcileExecVCSTestExact(t, observation)
	if len(reviews) != 1 || reviews[0].Action().Title() != "Edit" ||
		!strings.Contains(reviews[0].Diff, "-staged bytes") || !strings.Contains(reviews[0].Diff, "+committed bytes") {
		t.Fatalf("staged reset evidence = %+v; want staged bytes against HEAD", reviews)
	}
}

func TestGitRestoreAndCheckoutPathQueriesFromSubdirectory(t *testing.T) {
	for _, test := range []struct {
		name, command string
		args          []string
	}{
		{name: "restore", command: "git restore -- ../tracked.txt", args: []string{"restore", "--", "../tracked.txt"}},
		{name: "checkout", command: "git checkout -- ../tracked.txt", args: []string{"checkout", "--", "../tracked.txt"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := newExecVCSTestRepo(t, map[string]string{"tracked.txt": "committed bytes\n"})
			subdirectory := filepath.Join(repo, "subdir")
			if err := os.Mkdir(subdirectory, 0o700); err != nil {
				t.Fatal(err)
			}
			writeTestFile(t, filepath.Join(repo, "tracked.txt"), "worktree bytes\n")

			observation := captureExecVCSTestCommandAt(t, repo, subdirectory, test.command)
			assertCapturedExecVCSTestFile(t, observation, filepath.Join(repo, "tracked.txt"), "worktree bytes\n")
			assertExecVCSTestFileContent(t, filepath.Join(repo, "tracked.txt"), "worktree bytes\n")
			runExecVCSTestGitAt(t, repo, subdirectory, test.args...)

			reviews := reconcileExecVCSTestExact(t, observation)
			if len(reviews) != 1 || reviews[0].BeforePath != filepath.Join(repo, "tracked.txt") ||
				reviews[0].AfterPath != filepath.Join(repo, "tracked.txt") ||
				!strings.Contains(reviews[0].Diff, "-worktree bytes") || !strings.Contains(reviews[0].Diff, "+committed bytes") {
				t.Fatalf("%s from subdirectory = %+v; want exact root-relative path evidence", test.name, reviews)
			}
		})
	}
}

func TestGitCleanProviderExpandsDirectoriesAndPreservesDiscardedBytes(t *testing.T) {
	repo := newExecVCSTestRepo(t, map[string]string{"tracked.txt": "keep\n"})
	discarded := filepath.Join(repo, "untracked", "nested", "discarded.txt")
	writeTestFile(t, discarded, "untracked original bytes\n")

	observation := captureExecVCSTestCommand(t, repo, "git clean -fd")
	assertCapturedExecVCSTestFile(t, observation, discarded, "untracked original bytes\n")
	assertExecVCSTestFileContent(t, discarded, "untracked original bytes\n")
	runExecVCSTestGit(t, repo, "clean", "-fd")

	reviews := reconcileExecVCSTestExact(t, observation)
	if len(reviews) != 1 || reviews[0].Action().Title() != "Delete" ||
		reviews[0].BeforePath != discarded || reviews[0].AfterPath != "" ||
		!strings.Contains(reviews[0].Diff, "-untracked original bytes") {
		t.Fatalf("clean evidence = %+v; want exact deleted-file evidence", reviews)
	}
}

func TestGitApplyRenameProviderCapturesSourcePath(t *testing.T) {
	original := strings.Repeat("stable row\n", 12) + "old ending\n"
	updated := strings.Replace(original, "old ending\n", "new ending\n", 1)
	repo := newExecVCSTestRepo(t, map[string]string{"old-name.txt": original})
	runExecVCSTestGit(t, repo, "mv", "old-name.txt", "new-name.txt")
	writeTestFile(t, filepath.Join(repo, "new-name.txt"), updated)
	runExecVCSTestGit(t, repo, "add", "--all")
	patch := runExecVCSTestGit(t, repo, "diff", "--cached", "--find-renames")
	if !strings.Contains(string(patch), "rename from old-name.txt") || !strings.Contains(string(patch), "rename to new-name.txt") {
		t.Fatalf("fixture patch is not a rename: %q", patch)
	}
	patchPath := filepath.Join(t.TempDir(), "rename.patch")
	if err := os.WriteFile(patchPath, patch, 0o600); err != nil {
		t.Fatal(err)
	}
	runExecVCSTestGit(t, repo, "reset", "--hard", "HEAD")

	command := "git apply " + shellQuoteArgument(patchPath)
	observation := captureExecVCSTestCommand(t, repo, command)
	assertCapturedExecVCSTestFile(t, observation, filepath.Join(repo, "old-name.txt"), original)
	assertExecVCSTestFileContent(t, filepath.Join(repo, "old-name.txt"), original)
	if _, err := os.Stat(filepath.Join(repo, "new-name.txt")); !os.IsNotExist(err) {
		t.Fatalf("capture applied the rename: stat new path error = %v", err)
	}
	runExecVCSTestGit(t, repo, "apply", patchPath)

	reviews := reconcileExecVCSTestExact(t, observation)
	byPath := make(map[string]string, len(reviews))
	for _, review := range reviews {
		path := review.AfterPath
		if path == "" {
			path = review.BeforePath
		}
		byPath[filepath.Base(path)] = review.Action().Title() + "\n" + review.Diff
	}
	if len(reviews) != 2 || !strings.Contains(byPath["old-name.txt"], "Delete\n") ||
		!strings.Contains(byPath["old-name.txt"], "-old ending") ||
		!strings.Contains(byPath["new-name.txt"], "Create\n") || !strings.Contains(byPath["new-name.txt"], "+new ending") {
		t.Fatalf("rename evidence = %+v; want exact source deletion and destination creation", reviews)
	}
}

func TestGitProviderQueriesDisableConfiguredFSMonitor(t *testing.T) {
	repo := newExecVCSTestRepo(t, map[string]string{"tracked.txt": "committed\n"})
	marker := filepath.Join(t.TempDir(), "fsmonitor-ran")
	hook := filepath.Join(t.TempDir(), "fsmonitor-hook")
	hookText := "#!/bin/sh\nprintf 'ran\\n' >> " + shellQuoteArgument(marker) + "\nprintf 'test-token\\0'\n"
	if err := os.WriteFile(hook, []byte(hookText), 0o700); err != nil {
		t.Fatal(err)
	}
	runExecVCSTestGit(t, repo, "config", "core.fsmonitor", hook)
	runExecVCSTestGit(t, repo, "status", "--short")
	if _, err := os.Stat(marker); err != nil {
		t.Skipf("Git did not invoke the configured fsmonitor hook during fixture validation: %v", err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(repo, "tracked.txt"), "changed\n")
	observation := captureExecVCSTestCommand(t, repo, "git restore -- tracked.txt")
	assertCapturedExecVCSTestFile(t, observation, filepath.Join(repo, "tracked.txt"), "changed\n")
	assertExecVCSTestFileContent(t, filepath.Join(repo, "tracked.txt"), "changed\n")
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("provider query ran configured fsmonitor hook: stat error = %v", err)
	}
}

func TestGitProviderSkipsWorktreeQueryWhenCleanFilterConfigured(t *testing.T) {
	repo := newExecVCSTestRepo(t, map[string]string{
		".gitattributes": "tracked.txt filter=probe\n",
		"tracked.txt":    "committed\n",
	})
	marker := filepath.Join(t.TempDir(), "clean-filter-ran")
	filter := filepath.Join(t.TempDir(), "clean-filter")
	filterText := "#!/bin/sh\nprintf 'ran\\n' >> " + shellQuoteArgument(marker) + "\ncat\n"
	if err := os.WriteFile(filter, []byte(filterText), 0o700); err != nil {
		t.Fatal(err)
	}
	runExecVCSTestGit(t, repo, "config", "filter.probe.clean", filter)
	writeTestFile(t, filepath.Join(repo, "tracked.txt"), "changed\n")

	observation := captureExecVCSTestCommand(t, repo, "git restore -- tracked.txt")
	assertCapturedExecVCSTestFile(t, observation, filepath.Join(repo, "tracked.txt"), "changed\n")
	assertExecVCSTestFileContent(t, filepath.Join(repo, "tracked.txt"), "changed\n")
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("provider query ran configured clean filter: stat error = %v", err)
	}
	if !observation.Sweep || len(observation.Files) != 1 || filepath.Clean(observation.Files[0].Path) != filepath.Join(repo, "tracked.txt") {
		t.Fatalf("clean-filter fallback is not open and operand-only: %+v", observation)
	}
}

func newExecVCSTestRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	repo := t.TempDir()
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(repo, ".test-global-config"))
	runExecVCSTestGit(t, repo, "init", "--quiet")
	runExecVCSTestGit(t, repo, "config", "user.name", "Mekugi Test")
	runExecVCSTestGit(t, repo, "config", "user.email", "mekugi-test@example.invalid")
	for path, content := range files {
		writeTestFile(t, filepath.Join(repo, path), content)
	}
	runExecVCSTestGit(t, repo, "add", "--all")
	runExecVCSTestGit(t, repo, "commit", "--quiet", "-m", "initial")
	return repo
}

func runExecVCSTestGit(t *testing.T, repo string, args ...string) []byte {
	t.Helper()
	return runExecVCSTestGitAt(t, repo, repo, args...)
}

func runExecVCSTestGitAt(t *testing.T, repo, directory string, args ...string) []byte {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	command.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+filepath.Join(repo, ".test-global-config"),
		"GIT_TERMINAL_PROMPT=0",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return output
}

func captureExecVCSTestCommand(t *testing.T, repo, command string) *execObservation {
	t.Helper()
	return captureExecVCSTestCommandAt(t, repo, repo, command)
}

func captureExecVCSTestCommandAt(t *testing.T, repo, workdir, command string) *execObservation {
	t.Helper()
	observation, observed := captureExecObservation([]execCommandInput{{
		Command: command, Workdir: workdir, Shell: "bash",
	}}, false, false, execCaptureEnv{directory: repo})
	if !observed || observation == nil {
		t.Fatalf("VCS writer %q was not captured", command)
	}
	if observation.Class != execScoped.String() {
		t.Fatalf("VCS class for %q = %q, want scoped (reason: %s)", command, observation.Class, observation.Reason)
	}
	return observation
}

func assertCapturedExecVCSTestFile(t *testing.T, observation *execObservation, path, content string) {
	t.Helper()
	for _, file := range observation.Files {
		if filepath.Clean(file.Path) == filepath.Clean(path) {
			if file.Content != content {
				t.Fatalf("captured bytes for %s = %q, want %q", path, file.Content, content)
			}
			return
		}
	}
	t.Fatalf("pre-call scope did not capture %s: %+v", path, observation.Files)
}

func assertExecVCSTestFileContent(t *testing.T, path, want string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil || string(content) != want {
		t.Fatalf("file after capture %s = %q, %v; want %q", path, content, err, want)
	}
}

func reconcileExecVCSTestExact(t *testing.T, observation *execObservation) []mekugi.ReviewFile {
	t.Helper()
	reviews, complete, coverage, reason := reconcileExecObservation(*observation, execReconcileEnv{})
	if !complete || coverage != execCoverageExact {
		t.Fatalf("VCS review complete=%v coverage=%q reason=%q files=%+v; want exact", complete, coverage, reason, reviews)
	}
	return reviews
}
