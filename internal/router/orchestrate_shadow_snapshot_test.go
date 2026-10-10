package router

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/orchestrate"
)

func TestOrchestrateShadowPreparation(t *testing.T) {
	workspace := t.TempDir()
	writeTestFile(t, filepath.Join(workspace, ".gitignore"), "ignored/\n")
	writeTestFile(t, filepath.Join(workspace, ".gitattributes"), "*.txt text eol=crlf filter=forbidden\n")
	writeTestFile(t, filepath.Join(workspace, "raw.txt"), "raw\r\n")
	writeTestFile(t, filepath.Join(workspace, "image"), "binary\x00bytes")
	writeTestFile(t, filepath.Join(workspace, "ignored", "input"), "ignored input")
	writeTestFile(t, filepath.Join(workspace, "cache", ".svn", "entries"), "metadata")
	if err := os.Chmod(filepath.Join(workspace, "image"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("raw.txt", filepath.Join(workspace, "link")); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(t.TempDir(), "gitconfig")
	writeTestFile(t, config, "[filter \"forbidden\"]\n\tclean = /nonexistent/filter\n\tsmudge = /nonexistent/filter\n\trequired = true\n[commit]\n\tgpgSign = true\n[gpg]\n\tprogram = /nonexistent/sign\n")
	t.Setenv("GIT_CONFIG_GLOBAL", config)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	s := &orchestrate.Store{Directory: t.TempDir(), ShadowSnapshot: orchestrateShadowSnapshot}
	b, err := s.Prepare(t.Context(), workspace, "main", "batch")
	if err != nil || b.VCS != "shadow" || b.Base == "" {
		t.Fatal("shadow preparation failed", b, err)
	}
	for path, want := range map[string]string{"raw.txt": "raw\r\n", "image": "binary\x00bytes"} {
		if got, err := os.ReadFile(filepath.Join(b.Cwd, path)); err != nil || string(got) != want {
			t.Fatal("source bytes were not preserved", path, err)
		}
	}
	if link, err := os.Readlink(filepath.Join(b.Cwd, "link")); err != nil || link != "raw.txt" {
		t.Fatal("symlink was not preserved", link, err)
	}
	if info, err := os.Stat(filepath.Join(b.Cwd, "image")); err != nil || info.Mode()&0100 == 0 {
		t.Fatal("executable mode was not preserved", err)
	}
	for _, path := range []string{"ignored/input", "cache/.svn/entries"} {
		if _, err := os.Lstat(filepath.Join(b.Cwd, path)); !os.IsNotExist(err) {
			t.Fatal("excluded input entered the checkout", path, err)
		}
	}
	writeTestFile(t, filepath.Join(b.Cwd, "input"), "prepared input")
	writeTestFile(t, filepath.Join(workspace, "raw.txt"), "new source bytes")
	other, err := s.Prepare(t.Context(), workspace, "main", "other")
	if err != nil || other.Base == b.Base || other.Repository != b.Repository {
		t.Fatal("new snapshot did not retain the run repository", other, err)
	}
	repeat, err := (&orchestrate.Store{Directory: s.Directory}).Prepare(t.Context(), workspace, "main", "batch")
	if err != nil || repeat != b {
		t.Fatal("restart replaced the prepared baseline", repeat, err)
	}
	if got, err := os.ReadFile(filepath.Join(repeat.Cwd, "input")); err != nil || string(got) != "prepared input" {
		t.Fatal("prepared input changed", err)
	}
	if got, err := os.ReadFile(filepath.Join(workspace, "raw.txt")); err != nil || string(got) != "new source bytes" {
		t.Fatal("preparation changed source files", err)
	}
}

func TestOrchestrateShadowRejectsIncompleteBaseline(t *testing.T) {
	workspace := t.TempDir()
	nested := filepath.Join(workspace, "nested")
	writeTestFile(t, filepath.Join(nested, "file"), "nested")
	gitTestRun(t, nested, "init", "--quiet")
	gitTestRun(t, nested, "-c", "user.name=test", "-c", "user.email=test@example.com", "add", ".")
	gitTestRun(t, nested, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-qm", "base")
	s := &orchestrate.Store{Directory: t.TempDir(), ShadowSnapshot: orchestrateShadowSnapshot}
	b, err := s.Prepare(t.Context(), workspace, "main", "batch")
	if err == nil || b.State != "failed" || !strings.Contains(b.Error, "nested Git") {
		t.Fatal("incomplete snapshot authorized a checkout", b, err)
	}
	if _, err := s.Prepare(t.Context(), workspace, "main", "batch"); err == nil {
		t.Fatal("failed effect repeated")
	}
	if _, err := os.Stat(b.Checkout); !os.IsNotExist(err) {
		t.Fatal("failed snapshot created a checkout", err)
	}
}
