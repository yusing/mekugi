package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func workspaceFixture(t *testing.T, uses []string, modules map[string]string) {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "repository")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	var work strings.Builder
	work.WriteString("go 1.27.0\nuse (\n")
	for _, path := range uses {
		fmt.Fprintf(&work, "%q\n", path)
	}
	work.WriteString(")\n")
	for path, module := range modules {
		target := filepath.Join(directory, path)
		if err := os.MkdirAll(target, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(target, "go.mod"), []byte("module "+module+"\n\ngo 1.27.0\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(directory, "go.work"), []byte(work.String()), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(directory)
	t.Setenv("GOWORK", filepath.Join(directory, "go.work"))
}

func TestWorkspaceMembersExplicit(t *testing.T) {
	workspaceFixture(t, []string{".", "./libraries/first"}, map[string]string{
		".":                  prefix,
		"libraries/first":    `"` + prefix + `/libraries/first"`,
		"libraries/unlisted": prefix + "/libraries/unlisted",
	})
	got, err := workspaceMembers()
	want := []member{{".", prefix}, {"libraries/first", prefix + "/libraries/first"}}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("members=%v error=%v, want %v", got, err, want)
	}
}

func TestWorkspaceMembersRejectInvalid(t *testing.T) {
	for _, tt := range []struct {
		name    string
		uses    []string
		modules map[string]string
		reason  string
	}{
		{"empty", nil, nil, "no members"},
		{"wrong module path", []string{"./libraries/first"}, map[string]string{"libraries/first": "example.com/other"}, "module path"},
		{"missing module", []string{"./libraries/missing"}, nil, "go.mod"},
		{"absolute member", []string{t.TempDir()}, nil, "outside repository"},
		{"outside repository", []string{"../outside"}, map[string]string{"../outside": prefix + "/outside"}, "outside repository"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			workspaceFixture(t, tt.uses, tt.modules)
			if got, err := workspaceMembers(); err == nil || !strings.Contains(err.Error(), tt.reason) {
				t.Fatalf("members=%v error=%v, want %q", got, err, tt.reason)
			}
		})
	}
}

func TestExpandWorkspaceArguments(t *testing.T) {
	members := []member{{".", prefix}, {"libraries/first", prefix + "/libraries/first"}}
	for _, tt := range []struct {
		name    string
		args    []string
		members []member
		want    []string
	}{
		{"repeated marker", []string{"test", "-args=two words", "@workspace", "literal@workspace", "@workspace", "tail"}, members, []string{"test", "-args=two words", "./...", "./libraries/first/...", "literal@workspace", "./...", "./libraries/first/...", "tail"}},
		{"empty members", []string{"before", "@workspace", "after"}, nil, []string{"before", "after"}},
		{"no marker", []string{"two words"}, members, []string{"two words"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			original := slices.Clone(tt.args)
			got := expand(tt.args, tt.members)
			if !slices.Equal(got, tt.want) || !slices.Equal(tt.args, original) {
				t.Fatalf("expanded=%q args=%q, want %q", got, tt.args, tt.want)
			}
		})
	}
}

func TestWorkspaceRunErrors(t *testing.T) {
	workspaceFixture(t, []string{"."}, map[string]string{".": prefix})
	for _, args := range [][]string{nil, {"unknown"}, {"run"}, {"run", filepath.Join(t.TempDir(), "missing-command")}} {
		if err := run(args); err == nil {
			t.Fatalf("run(%q) succeeded", args)
		}
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MEKUGI_WORKSPACE_EXIT_FIXTURE", "1")
	err = run([]string{"run", executable, "-test.run=^TestWorkspaceRunnerExitFixture$"})
	if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 7 {
		t.Fatalf("child error=%v, want exit 7", err)
	}
}

func TestWorkspaceRunnerExitFixture(t *testing.T) {
	if os.Getenv("MEKUGI_WORKSPACE_EXIT_FIXTURE") == "1" {
		os.Exit(7)
	}
}
