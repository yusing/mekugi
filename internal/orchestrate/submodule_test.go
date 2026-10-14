package orchestrate

import (
	"encoding/json/jsontext"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPrepareGitSubmodules(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(dir, path, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, path), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	init := func() string {
		t.Helper()
		dir := t.TempDir()
		run(dir, "init", "-q")
		run(dir, "config", "user.name", "test")
		run(dir, "config", "user.email", "test@example.com")
		write(dir, "file", "base")
		write(dir, ".gitignore", "input\n")
		run(dir, "add", ".")
		run(dir, "commit", "-qm", "base")
		return dir
	}
	leaf, module, source := init(), init(), init()
	run(module, "-c", "protocol.file.allow=always", "submodule", "add", "-q", leaf, "deep module")
	run(module, "commit", "-qam", "nested")
	run(source, "-c", "protocol.file.allow=always", "submodule", "add", "-q", module, "lib module")
	run(source, "-c", "protocol.file.allow=always", "submodule", "update", "--init", "--recursive")
	local := filepath.Join(source, "lib module")
	nested := filepath.Join(local, "deep module")
	base, leafBase := run(local, "rev-parse", "HEAD"), run(nested, "rev-parse", "HEAD")
	write(run(nested, "rev-parse", "--absolute-git-dir"), "objects/info/alternates", filepath.Join(leaf, ".git", "objects")+"\n")
	// A committed but uninitialized module must not trigger URL transport.
	run(source, "update-index", "--add", "--cacheinfo", "160000,"+base+",cold")
	write(source, ".gitmodules", "[submodule \"lib module\"]\npath = lib module\nurl = https://invalid.invalid/unreachable\n[submodule \"cold\"]\npath = cold\nurl = https://invalid.invalid/cold\n")
	run(source, "add", ".gitmodules")
	run(source, "commit", "-qm", "modules")
	// The source's checked-out submodule is newer and dirty; the batch still
	// starts from the superproject's recorded gitlinks, including nested ones.
	write(local, "file", "new commit")
	run(local, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-qam", "newer")
	write(local, "file", "dirty source")
	run(local, "add", "file")
	write(nested, "file", "nested dirty")
	beforeEdits := run(local, "diff", "--cached") + run(nested, "diff")
	before := run(source, "status", "--porcelain", "--ignore-submodules=none") + run(local, "diff", "--cached") + run(nested, "diff")
	s := &Store{Directory: t.TempDir()}
	b, err := s.Prepare(t.Context(), source, "main", "batch")
	if err != nil || len(b.Submodules) != 2 || b.Submodules[0].Base != base || b.Submodules[1].Base != leafBase {
		t.Fatal("submodule preparation", b, err)
	}
	for _, sub := range b.Submodules {
		dir := filepath.Join(b.Checkout, sub.Path)
		if got, err := os.ReadFile(filepath.Join(dir, "file")); err != nil || string(got) != "base" {
			t.Fatal("submodule copied source edits", sub, err)
		}
		if run(dir, "branch", "--show-current") != b.Branch || run(dir, "rev-parse", "HEAD") != sub.Base {
			t.Fatal("submodule branch or baseline mismatch", sub)
		}
		if run(dir, "rev-parse", "--path-format=absolute", "--git-common-dir") != filepath.Join(dir, ".git") {
			t.Fatal("submodule shares repository state", sub)
		}
		if _, err := os.Stat(filepath.Join(dir, ".git", "objects", "info", "alternates")); !os.IsNotExist(err) {
			t.Fatal("submodule retains source object dependencies", sub, err)
		}
	}
	if run(b.Cwd, "status", "--porcelain", "--ignore-submodules=none") != "" {
		t.Fatal("prepared superproject is dirty")
	}
	child := filepath.Join(b.Checkout, "lib module")
	write(child, "input", "copied input")
	reopened := &Store{Directory: s.Directory}
	repeat, err := reopened.Prepare(t.Context(), source, "main", "batch")
	if err != nil || !reflect.DeepEqual(repeat, b) {
		t.Fatal("reopened preparation changed submodule ownership", repeat, err)
	}
	if got, err := os.ReadFile(filepath.Join(child, "input")); err != nil || string(got) != "copied input" {
		t.Fatal("repeat replaced prepared input", err)
	}
	if after := run(source, "status", "--porcelain", "--ignore-submodules=none") + run(local, "diff", "--cached") + run(nested, "diff"); after != before {
		t.Fatal("source submodule state changed")
	}
	// Launch checks all local clone identities before authorizing a host effect.
	run(child, "checkout", "--detach", "-q")
	if _, dispatch, err := reopened.BeginLaunch(t.Context(), source, "main", "batch", "work", jsontext.Value(`{}`), nil, nil); err == nil || dispatch {
		t.Fatal("redirected submodule branch authorized launch")
	}
	run(child, "checkout", "-q", b.Branch)
	if _, dispatch, err := reopened.BeginLaunch(t.Context(), source, "main", "batch", "work", jsontext.Value(`{}`), nil, nil); err != nil || !dispatch {
		t.Fatal("prepared submodules rejected launch", err)
	}
	if err := reopened.RecordThread(t.Context(), source, "main", "batch", "child"); err != nil {
		t.Fatal(err)
	}
	// A .gitmodules ignore setting cannot hide unfinished submodule edits
	// from the Main-only integration acceptance check.
	run(b.Cwd, "config", "submodule.lib module.ignore", "all")
	childNested := filepath.Join(child, "deep module")
	run(child, "config", "submodule.deep module.ignore", "all")
	write(childNested, "file", "unfinished nested work")
	if _, err := reopened.RecordIntegration(t.Context(), source, "main", "batch", "child"); err == nil {
		t.Fatal("accepted hidden nested submodule edits")
	}
	// Commit nested work, then integrate only the superproject. Its gitlinks
	// name objects that still exist only in the batch's isolated clones.
	for _, dir := range []string{childNested, child, b.Checkout} {
		run(dir, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-qam", "child work")
	}
	tip := run(b.Checkout, "rev-parse", "HEAD")
	run(source, "cherry-pick", tip)
	// Each locally imported parent must expose its nested gitlink from the
	// source repository, rather than letting the child's clone supply proof.
	for _, sub := range b.Submodules {
		clone := filepath.Join(b.Checkout, sub.Path)
		subTip := run(clone, "rev-parse", "HEAD")
		if _, err := git(t.Context(), sub.Repository, "--no-lazy-fetch", "cat-file", "-e", subTip+"^{commit}"); err == nil {
			t.Fatal("fixture source already contains child object", sub.Path)
		}
		if _, err := reopened.RecordIntegration(t.Context(), source, "main", "batch", "child"); err == nil {
			t.Fatal("accepted source gitlink without its local commit object", sub.Path)
		} else if !strings.Contains(err.Error(), sub.Path) {
			t.Fatal("missing-object error does not identify submodule", sub.Path, err)
		}
		run(sub.Repository, "-c", "protocol.file.allow=always", "fetch", "--no-recurse-submodules", "--no-write-fetch-head", clone, subTip)
	}
	proof, err := reopened.RecordIntegration(t.Context(), source, "main", "batch", "child")
	if err != nil || proof.Tip != tip || proof.SourceTip != run(source, "rev-parse", "HEAD") {
		t.Fatal("rejected locally imported submodule objects", proof, err)
	}
	if run(local, "diff", "--cached")+run(nested, "diff") != beforeEdits {
		t.Fatal("integration changed source submodule edits")
	}
}
