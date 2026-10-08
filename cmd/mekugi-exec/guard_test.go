package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestGuardEnvironmentMiseShimIdentity(t *testing.T) {
	root := t.TempDir()
	alias := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	manager, shim := filepath.Join(alias, "mise"), filepath.Join(alias, "bash")
	if err := os.WriteFile(manager, nil, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(manager, shim); err != nil {
		t.Fatal(err)
	}
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	active, inherited := filepath.Join(root, "active", "vcs-guard"), filepath.Join(root, "inherited", "vcs-guard")
	for _, directory := range []string{active, inherited} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(helper, filepath.Join(active, "bash")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inherited, "bash"), nil, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", active+":"+inherited+":"+root)
	t.Setenv("__MISE_SHIM_PATH", "original-shim")
	original := os.Environ()
	if got := guardEnvironment(manager); !slices.Equal(got, original) {
		t.Fatal("ordinary executable environment changed")
	}
	got := guardEnvironment(shim)
	if !slices.Contains(got, "__MISE_SHIM_PATH="+helper) || !slices.Contains(got, "PATH="+active+":"+root) {
		t.Fatal("shim identity or inherited guard filtering missing")
	}
	if !slices.Equal(os.Environ(), original) {
		t.Fatal("invocation-local adjustment changed parent environment")
	}
}
