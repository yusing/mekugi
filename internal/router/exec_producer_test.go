package router

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func execProducerCommand(t *testing.T, name string) string {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s runtime is unavailable", name)
	}
	return name
}

func execProducerCapture(t *testing.T, root, command string) *execObservation {
	t.Helper()
	observation, ok := captureExecObservation(
		[]execCommandInput{{Command: command, Workdir: root, Shell: "bash"}},
		false, false, execCaptureEnv{directory: root, clock: t.TempDir()},
	)
	if !ok {
		t.Fatalf("%q was not observed", command)
	}
	return observation
}

func execProducerAssertCaptured(t *testing.T, observation *execObservation, root string, expected map[string]string) {
	t.Helper()
	if observation.Class != execScoped.String() {
		t.Fatalf("capture class = %q (%s), want scoped", observation.Class, observation.Reason)
	}
	captured := make(map[string]string, len(observation.Files))
	for _, file := range observation.Files {
		captured[file.Path] = file.Content
	}
	for relative, want := range expected {
		path := filepath.Join(root, relative)
		if got, ok := captured[path]; !ok || got != want {
			t.Errorf("pre-call content for %s = %q (present %v), want %q; captured %v", relative, got, ok, want, captured)
		}
	}
}

func execProducerRun(t *testing.T, bash, root, command string) {
	t.Helper()
	process := exec.Command(bash, "-c", command)
	process.Dir = root
	if output, err := process.CombinedOutput(); err != nil {
		t.Fatalf("run %q: %v\n%s", command, err, output)
	}
}

func execProducerAssertExactDirect(t *testing.T, root string, observation *execObservation, want []string) {
	t.Helper()
	reviews, complete, coverage, unswept := reconcileExecObservation(*observation, execReconcileEnv{})
	if !complete || coverage != execCoverageExact || unswept != "" {
		t.Fatalf("reconciliation complete=%v coverage=%q unswept=%q reviews=%+v", complete, coverage, unswept, reviews)
	}
	got := make([]string, 0, len(reviews))
	for _, review := range reviews {
		path := review.AfterPath
		if path == "" {
			path = review.BeforePath
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatalf("review path %q is outside root %q: %v", path, root, err)
		}
		got = append(got, review.Action().Title()+" "+filepath.ToSlash(relative))
		if !strings.Contains(review.Diff, "old") || !strings.Contains(review.Diff, "new") {
			t.Errorf("review does not contain before/after text: %+v", review)
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("reviews = %q, want %q", got, want)
	}
	for _, review := range reviews {
		if review.Origin != "" {
			t.Errorf("direct file effect unexpectedly has tool-managed origin %q: %+v", review.Origin, review)
		}
	}
}

func TestExecFindExecProducerCapturesBeforeContent(t *testing.T) {
	find := execProducerCommand(t, "find")
	sed := execProducerCommand(t, "sed")
	bash := execProducerCommand(t, "bash")
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "src", "a.txt"), "old old\n")
	writeTestFile(t, filepath.Join(root, "src", "nested", "b.txt"), "still old\n")
	writeTestFile(t, filepath.Join(root, "src", "skip.md"), "old\n")
	command := find + " src -name '*.txt' -exec " + sed + " -i 's/old/new/g' {} \\;"
	observation := execProducerCapture(t, root, command)
	execProducerAssertCaptured(t, observation, root, map[string]string{
		"src/a.txt":        "old old\n",
		"src/nested/b.txt": "still old\n",
	})
	if got, err := os.ReadFile(filepath.Join(root, "src", "a.txt")); err != nil || string(got) != "old old\n" {
		t.Fatalf("capture executed the writer: %q, %v", got, err)
	}
	execProducerRun(t, bash, root, command)
	if got, err := os.ReadFile(filepath.Join(root, "src", "a.txt")); err != nil || string(got) != "new new\n" {
		t.Fatalf("writer result = %q, %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(root, "src", "skip.md")); err != nil || string(got) != "old\n" {
		t.Fatalf("nonmatching file changed: %q, %v", got, err)
	}
	execProducerAssertExactDirect(t, root, observation, []string{"Edit src/a.txt", "Edit src/nested/b.txt"})
}

func TestExecRGXargsProducerCapturesBeforeContent(t *testing.T) {
	rg := execProducerCommand(t, "rg")
	xargs := execProducerCommand(t, "xargs")
	sed := execProducerCommand(t, "sed")
	bash := execProducerCommand(t, "bash")
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "src", "a.txt"), "old old\n")
	writeTestFile(t, filepath.Join(root, "src", "nested", "b.txt"), "still old\n")
	writeTestFile(t, filepath.Join(root, "src", "unchanged.txt"), "nothing here\n")
	command := rg + " -l old src | " + xargs + " " + sed + " -i 's/old/new/g'"
	observation := execProducerCapture(t, root, command)
	execProducerAssertCaptured(t, observation, root, map[string]string{
		"src/a.txt":        "old old\n",
		"src/nested/b.txt": "still old\n",
	})
	if got, err := os.ReadFile(filepath.Join(root, "src", "a.txt")); err != nil || string(got) != "old old\n" {
		t.Fatalf("capture executed the writer: %q, %v", got, err)
	}
	execProducerRun(t, bash, root, command)
	if got, err := os.ReadFile(filepath.Join(root, "src", "a.txt")); err != nil || string(got) != "new new\n" {
		t.Fatalf("writer result = %q, %v", got, err)
	}
	execProducerAssertExactDirect(t, root, observation, []string{"Edit src/a.txt", "Edit src/nested/b.txt"})
}

func TestExecRGPreProducerIsOpaqueWithoutExecutingHook(t *testing.T) {
	rg := execProducerCommand(t, "rg")
	sed := execProducerCommand(t, "sed")
	root := t.TempDir()
	marker := filepath.Join(root, "hook-ran")
	writeTestFile(t, filepath.Join(root, "src", "hit.txt"), "old\n")
	hook := filepath.Join(root, "hook")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\n: > \"$MARKER\"\ncat \"$1\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MARKER", marker)
	command := rg + " --pre ./hook -l old src | xargs " + sed + " -i 's/old/new/g'"
	plan := classifyExecShell(command, root, "bash")
	if plan.Class != execOpaque {
		t.Fatalf("hostile producer class = %v (%s), want opaque", plan.Class, plan.Reason)
	}
	observation := execProducerCapture(t, root, command)
	if observation.Class != execOpaque.String() {
		t.Fatalf("hostile producer capture class = %q (%s), want opaque", observation.Class, observation.Reason)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("hostile --pre hook ran during classification/capture (stat err %v)", err)
	}
	if got, err := os.ReadFile(filepath.Join(root, "src", "hit.txt")); err != nil || string(got) != "old\n" {
		t.Fatalf("writer ran during classification/capture: %q, %v", got, err)
	}
}
