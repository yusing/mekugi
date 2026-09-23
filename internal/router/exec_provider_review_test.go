package router

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func execProviderReviewMarker(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	marker := filepath.Join(bin, "invoked")
	program := filepath.Join(bin, "marker")
	script := "#!/bin/sh\nprintf 'invoked\\n' >> " + shellQuoteArgument(marker) + "\n"
	if err := os.WriteFile(program, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return marker
}

func assertExecProviderReviewMarkerDidNotRun(t *testing.T, marker string) {
	t.Helper()
	if data, err := os.ReadFile(marker); err == nil {
		t.Fatalf("untrusted producer command ran during scope capture: %q", data)
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat producer marker: %v", err)
	}
}

func TestExecRGHostnameCommandIsNeverRunDuringScoping(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("rg is unavailable")
	}
	if _, err := exec.LookPath("xargs"); err != nil {
		t.Skip("xargs is unavailable")
	}
	marker := execProviderReviewMarker(t)
	rg := execProducerCommand(t, "rg")
	xargs := execProducerCommand(t, "xargs")
	sed := execProducerCommand(t, "sed")
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "PAT"), "program PAT\n")
	writeTestFile(t, filepath.Join(root, "FILE"), "program FILE\n")
	command := rg + " --hostname-bin=marker program --hyperlink-format='file://{host}{path}' --color=always -l PAT FILE | " +
		xargs + " " + sed + " -i 's/program/replaced/g'"
	observation := execProducerCapture(t, root, command)
	if observation.Class != execOpaque.String() {
		t.Fatalf("hostname executable producer classified as %q (%s), want opaque", observation.Class, observation.Reason)
	}
	assertExecProviderReviewMarkerDidNotRun(t, marker)
}

func TestExecFDExecBundleIsNeverRunDuringScoping(t *testing.T) {
	if _, err := exec.LookPath("fd"); err != nil {
		t.Skip("fd is unavailable")
	}
	if _, err := exec.LookPath("xargs"); err != nil {
		t.Skip("xargs is unavailable")
	}
	marker := execProviderReviewMarker(t)
	fd := execProducerCommand(t, "fd")
	xargs := execProducerCommand(t, "xargs")
	sed := execProducerCommand(t, "sed")
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "src", "PAT.txt"), "old bytes\n")
	command := fd + " PAT src -Hx marker program | " + xargs + " " + sed + " -i 's/old/new/g'"
	observation := execProducerCapture(t, root, command)
	if observation.Class != execOpaque.String() {
		t.Fatalf("fd bundled exec producer classified as %q (%s), want opaque", observation.Class, observation.Reason)
	}
	assertExecProviderReviewMarkerDidNotRun(t, marker)
	if data, err := os.ReadFile(filepath.Join(root, "src", "PAT.txt")); err != nil || string(data) != "old bytes\n" {
		t.Fatalf("fd writer ran during scope capture: %q, %v", data, err)
	}
}

func TestExecFindPathProducerCapturesBaseline(t *testing.T) {
	find := execProducerCommand(t, "find")
	sed := execProducerCommand(t, "sed")
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "src", "one.txt"), "old baseline\n")
	writeTestFile(t, filepath.Join(root, "src", "nested", "two.txt"), "old nested\n")
	command := find + " . -path './src/*.txt' -exec " + sed + " -i 's/old/new/g' {} \\;"
	observation := execProducerCapture(t, root, command)
	execProducerAssertCaptured(t, observation, root, map[string]string{"src/one.txt": "old baseline\n"})
	if data, err := os.ReadFile(filepath.Join(root, "src", "one.txt")); err != nil || string(data) != "old baseline\n" {
		t.Fatalf("find writer ran during scope capture: %q, %v", data, err)
	}
}

func TestExecPythonUnresolvedReasonSurvivesClosedStatement(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "closed.txt"), "old baseline\n")
	writeTestFile(t, filepath.Join(root, "partial.txt"), "partial baseline\n")
	command := `python3 -c 'open("closed.txt", "w").write("new")' && python3 -c 'open("partial.txt", "w").write("new"); open(target, "w").write("new")'`
	observation := execProducerCapture(t, root, command)
	if observation.Class != execScoped.String() || !strings.Contains(observation.Reason, "unresolved") {
		t.Fatalf("later scoped-but-unresolved statement lost its reason: class=%q reason=%q scope=%+v", observation.Class, observation.Reason, observation.Files)
	}
	execProducerAssertCaptured(t, observation, root, map[string]string{
		"closed.txt":  "old baseline\n",
		"partial.txt": "partial baseline\n",
	})
}
