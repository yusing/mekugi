package router

import (
	"crypto/sha256"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/orchestrate"
)

func svnTestRun(t *testing.T, args ...string) string {
	t.Helper()
	command := exec.CommandContext(t.Context(), "svn", append([]string{"--non-interactive", "--config-dir", t.TempDir()}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("svn %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func orchestrateSVNWorkspace(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("svnadmin"); err != nil {
		t.Skip("SVN client and svnadmin are required")
	}
	repository := filepath.Join(t.TempDir(), "repo")
	if output, err := exec.CommandContext(t.Context(), "svnadmin", "create", repository).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin: %v\n%s", err, output)
	}
	workspace := filepath.Join(t.TempDir(), "working copy")
	svnTestRun(t, "checkout", "-q", (&url.URL{Scheme: "file", Path: repository}).String(), workspace)
	writeTestFile(t, filepath.Join(workspace, "file"), "base")
	svnTestRun(t, "add", "-q", filepath.Join(workspace, "file"))
	svnTestRun(t, "commit", "-q", "-m", "baseline", workspace)
	// The root is still r0; its per-path committed baselines include file at r1.
	return workspace
}

func svnMetadata(t *testing.T, workspace string) map[string][32]byte {
	t.Helper()
	root := filepath.Join(workspace, ".svn")
	digests := make(map[string][32]byte)
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		digests[strings.TrimPrefix(path, root)] = sha256.Sum256(data)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return digests
}

func TestOrchestrateSVNBaselineAndIntegration(t *testing.T) {
	workspace := orchestrateSVNWorkspace(t)
	// The closest SVN owner wins even inside an unrelated Git checkout.
	gitTestRun(t, filepath.Dir(workspace), "init", "--quiet")
	base := "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\n"
	for path, data := range map[string]string{"sub/file": base, "old": "original", "replaced": "original", "deleted": "original", "ignored": "versioned", "script": "run", ".gitignore": "ignored\n", "binary": "base\x00bytes"} {
		writeTestFile(t, filepath.Join(workspace, path), data)
	}
	if err := os.Symlink("file", filepath.Join(workspace, "link")); err != nil {
		t.Fatal(err)
	}
	svnTestRun(t, "add", "--force", workspace)
	svnTestRun(t, "propset", "svn:executable", "*", filepath.Join(workspace, "script"))
	svnTestRun(t, "commit", "-q", "-m", "more baselines", workspace)
	// File externals have local committed metadata; directory externals do not.
	svnTestRun(t, "update", "--depth", "empty", workspace)
	svnTestRun(t, "propset", "svn:externals", svnTestRun(t, "info", "--show-item", "url", workspace)+"/file external", workspace)
	svnTestRun(t, "commit", "-q", "-m", "external baseline", workspace)
	svnTestRun(t, "update", workspace)
	writeTestFile(t, filepath.Join(workspace, "external"), "local external edit")
	svnTestRun(t, "move", filepath.Join(workspace, "old"), filepath.Join(workspace, "moved"))
	svnTestRun(t, "delete", filepath.Join(workspace, "replaced"), filepath.Join(workspace, "deleted"))
	writeTestFile(t, filepath.Join(workspace, "replaced"), "local replacement")
	writeTestFile(t, filepath.Join(workspace, "added", "file"), "local addition")
	svnTestRun(t, "add", filepath.Join(workspace, "replaced"), filepath.Join(workspace, "added"))
	svnTestRun(t, "propdel", "svn:executable", filepath.Join(workspace, "script"))
	sourceText := strings.Replace(base, "nine", "source", 1)
	writeTestFile(t, filepath.Join(workspace, "sub", "file"), sourceText)
	before := svnMetadata(t, workspace)
	status := svnTestRun(t, "status", workspace)
	u := orchestrateCleanupUIInWorkspace(t, filepath.Join(workspace, "sub"))
	b := u.orchestrateThreads["child"].batch
	if b.VCS != "svn" || b.Source != workspace || b.Cwd != filepath.Join(b.Checkout, "sub") {
		t.Fatal("SVN workspace identity", b)
	}
	for path, want := range map[string]string{"sub/file": base, "old": "original", "replaced": "original", "deleted": "original", "ignored": "versioned", "binary": "base\x00bytes", "external": "base"} {
		if data, err := os.ReadFile(filepath.Join(b.Checkout, path)); err != nil || string(data) != want {
			t.Fatal("committed baseline lost", path, string(data), err)
		}
	}
	for _, path := range []string{".svn", "moved", "added"} {
		if _, err := os.Lstat(filepath.Join(b.Checkout, path)); !os.IsNotExist(err) {
			t.Fatal("local or metadata path entered baseline", path, err)
		}
	}
	if target, err := os.Readlink(filepath.Join(b.Checkout, "link")); err != nil || target != "file" {
		t.Fatal("committed link lost", target, err)
	}
	if info, err := os.Stat(filepath.Join(b.Checkout, "script")); err != nil || info.Mode().Perm()&0111 == 0 {
		t.Fatal("committed executable property lost", info, err)
	}
	gitTestRun(t, b.Cwd, "config", "user.name", "test")
	gitTestRun(t, b.Cwd, "config", "user.email", "test@example.invalid")
	writeTestFile(t, filepath.Join(b.Cwd, "file"), strings.Replace(base, "one", "child", 1))
	gitTestCommit(t, b.Cwd)
	orchestrateTargetMCPClient(t, u, "integrate")("main", false)
	if got, err := os.ReadFile(filepath.Join(workspace, "sub", "file")); err != nil || string(got) != strings.Replace(sourceText, "one", "child", 1) {
		t.Fatal("SVN merge lost source or child edit", string(got), err)
	}
	if !reflect.DeepEqual(before, svnMetadata(t, workspace)) || status != svnTestRun(t, "status", workspace) {
		t.Fatal("preparation or writeback changed SVN metadata and scheduling")
	}
	reopened := &orchestrate.Store{Directory: u.proxy.orchestration.store.Directory, ShadowSnapshot: orchestrateShadowSnapshot}
	if _, err := reopened.Resume(t.Context(), u.session.cwd, "main", "batch"); err != nil {
		t.Fatal("SVN restart cannot resume", err)
	}
	if _, err := u.proxy.applyJournal(u.ctx, u.session.cwd, "main", "", []journalMutation{{Op: "set", P: "/1", State: new("accepted")}}); err != nil {
		t.Fatal("SVN integration cannot be accepted", err)
	}
}

func TestOrchestrateSVNPreparationFailure(t *testing.T) {
	for _, kind := range []string{"local_directory", "missing_pristine"} {
		t.Run(kind, func(t *testing.T) {
			workspace := orchestrateSVNWorkspace(t)
			selected := workspace
			if kind == "local_directory" {
				selected = filepath.Join(workspace, "added")
				writeTestFile(t, filepath.Join(selected, "input"), "local")
				svnTestRun(t, "add", selected)
			} else if err := os.Rename(filepath.Join(workspace, ".svn", "pristine"), filepath.Join(workspace, ".svn", "retained-pristine")); err != nil {
				t.Fatal(err)
			}
			s := &orchestrate.Store{Directory: t.TempDir(), ShadowSnapshot: orchestrateShadowSnapshot, SVNBaseline: orchestrateSVNBaseline}
			b, err := s.Prepare(t.Context(), selected, "main", "batch")
			if err == nil || b.State != "failed" {
				t.Fatal("incomplete committed baseline prepared", b, err)
			}
			if _, err := os.Stat(b.Checkout); !os.IsNotExist(err) {
				t.Fatal("failed preparation created checkout", err)
			}
			if data, err := os.ReadFile(filepath.Join(workspace, "file")); err != nil || string(data) != "base" {
				t.Fatal("failed preparation changed source", string(data), err)
			}
		})
	}
}
