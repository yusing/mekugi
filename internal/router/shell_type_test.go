package router

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShellTypeAll(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	first, second := filepath.Join(directory, "first"), filepath.Join(directory, "second")
	for _, path := range []string{first, second} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"example", "mcat"} {
			if err := os.WriteFile(filepath.Join(path, name), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
				t.Fatal(err)
			}
		}
	}
	invocation := newShellWorkerTestInvocation(directory, "PATH="+first+string(os.PathListSeparator)+second)
	matches := "example is " + filepath.Join(first, "example") + "\nexample is " + filepath.Join(second, "example") + "\n"
	mcatMatches := "mcat is " + registry.frontends["mcat"] + "\n" +
		"mcat is " + filepath.Join(first, "mcat") + "\n" +
		"mcat is " + filepath.Join(second, "mcat") + "\n"
	for _, tc := range []struct {
		script, want string
		status       int
	}{
		{`type -a example`, matches, 0},
		{`example() { :; }; type -a example`, "example is a function\n" + matches, 0},
		{`example() { :; }; type -at example`, "function\nfile\nfile\n", 0},
		{`type -ap example`, filepath.Join(first, "example") + "\n" + filepath.Join(second, "example") + "\n", 0},
		{`type -a mcat`, mcatMatches, 0},
		{`type -a missing example`, matches, 1},
		{`type -az example`, "", 2},
		{`type -a '$(printf unsafe)'`, "", 1},
		{`type -a if`, "if is a shell keyword\n", 0},
		{`type -a example; type() { printf custom; }; type -a example`, matches + "custom", 0},
		{`(example() { :; }; type -at example)`, "function\nfile\nfile\n", 0},
	} {
		t.Run(tc.script, func(t *testing.T) {
			stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil, tc.script, nil, invocation)
			if stdout != tc.want || status != tc.status {
				t.Fatalf("got (%q, %q, %d), want (%q, %d)", stdout, stderr, status, tc.want, tc.status)
			}
			if status == 0 && strings.TrimSpace(stderr) != "" {
				t.Fatalf("unexpected stderr: %q", stderr)
			}
		})
	}
}
