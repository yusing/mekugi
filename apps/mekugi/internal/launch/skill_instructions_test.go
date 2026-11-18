package launch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSkillsManagerInExtraPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	environment := []string{"PATH=" + t.TempDir()}
	workspace := t.TempDir()
	directory := t.TempDir()
	path := filepath.Join(directory, "skills-mgr")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if !skillsManagerInPath(environment, workspace, directory) {
		t.Fatal("skills-mgr executable in frontend directory was not found")
	}
	if skillsManagerInPath(environment, workspace, t.TempDir()) {
		t.Fatal("skills-mgr reported available outside PATH")
	}
}

func TestSkillsManagerInRelativePath(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "skills-mgr")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	relative, err := filepath.Rel(workspace, directory)
	if err != nil {
		t.Fatal(err)
	}
	// The daemon's PATH and cwd must not decide an invocation's capabilities.
	t.Setenv("PATH", directory)
	if !skillsManagerInPath([]string{"PATH=" + relative}, workspace, "") {
		t.Fatal("skills-mgr executable in invocation-relative PATH was not found")
	}
	if skillsManagerInPath([]string{"PATH=" + t.TempDir()}, workspace, "") {
		t.Fatal("skills-mgr from a different environment was reported available")
	}
}
