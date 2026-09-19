package router

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestShellCommandRouting(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	for name, source := range map[string]string{
		"git": "#!/bin/sh\nprintf 'raw:%s\\n' \"$*\"\n",
		"rtk": "#!/bin/sh\nprintf 'routed:%s\\n' \"$*\"\nprintf 'extra\\n'\nexit 7\n",
	} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(source), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	invocation := newShellWorkerTestInvocation(directory, "PATH="+directory)
	for _, interpreter := range []string{"bash", "sh"} {
		for _, test := range []struct {
			name, script, want string
			status             int
		}{
			{"direct", "git status", "routed:git status\nextra\n", 7},
			{"expanded", "arg=status; git \"$arg\"", "routed:git status\nextra\n", 7},
			{"before hrun", "hrun -n 1 -- git status", "routed:git status\n", 7},
			{"already routed", "rtk git status", "routed:git status\nextra\n", 7},
			{"unsupported", "git rev-parse HEAD", "raw:rev-parse HEAD\n", 0},
			{"machine output", "git status --porcelain", "raw:status --porcelain\n", 0},
			{"substitution", `value=$(git status); printf '%s\n' "$value"`, "raw:status\n", 0},
			{"pipeline", "git status | /bin/cat", "raw:status\n", 0},
			{"pipeline consumer", "printf input | git status", "raw:status\n", 0},
			{"redirect", "git status > result; /bin/cat result", "raw:status\n", 0},
			{"stderr redirect", "git status 2> errors", "raw:status\n", 0},
			{"explicit path", filepath.Join(directory, "git") + " status", "raw:status\n", 0},
		} {
			t.Run(interpreter+"/"+test.name, func(t *testing.T) {
				stdout, stderr, status := runShellWorkerTest(t, registry, interpreter, nil, test.script, nil, invocation)
				if stdout != test.want || status != test.status {
					t.Fatalf("output/status = %q, %q, %d; want %q, %d", stdout, stderr, status, test.want, test.status)
				}
			})
		}
	}
}

func TestShellCommandRoutingAssignmentsAndWrappers(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	for name, source := range map[string]string{
		"rtk": "#!/bin/sh\nprintf 'routed\\n'\nexec \"$@\"\n",
		"go":  "#!/bin/sh\nprintf 'env=<%s>\\n' \"$FOO\"\nprintf 'arg=<%s>\\n' \"$@\"\nexit 9\n",
	} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(source), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	path := directory + string(os.PathListSeparator) + os.Getenv("PATH")
	invocation := newShellWorkerTestInvocation(directory, "PATH="+path)
	for _, interpreter := range []string{"bash", "sh"} {
		for _, test := range []struct {
			name, script, want string
		}{
			{"assignment", "FOO=bar go test ./pkg", "routed\nenv=<bar>\narg=<test>\narg=<./pkg>\n"},
			{"timeout", "timeout 120 go test ./pkg", "routed\nenv=<>\narg=<test>\narg=<./pkg>\n"},
			{"timeout flags", "timeout --signal=TERM 120 go test ./pkg", "routed\nenv=<>\narg=<test>\narg=<./pkg>\n"},
			{"nice", "nice -n 5 go test ./pkg", "routed\nenv=<>\narg=<test>\narg=<./pkg>\n"},
			{"env", "env FOO=bar go test ./pkg", "routed\nenv=<bar>\narg=<test>\narg=<./pkg>\n"},
			{"env separator", "env -- FOO=bar go test ./pkg", "routed\nenv=<bar>\narg=<test>\narg=<./pkg>\n"},
			{"wrapped machine output", "env FOO=bar go test -json ./pkg", "env=<bar>\narg=<test>\narg=<-json>\narg=<./pkg>\n"},
			{"wrapped pipeline", "timeout 120 go test ./pkg | /bin/cat", "env=<>\narg=<test>\narg=<./pkg>\n"},
			{"wrapped redirect", "env FOO=bar go test ./pkg > /dev/null; printf done", "done"},
			{"wrapped explicit path", "env FOO=bar " + filepath.Join(directory, "go") + " test ./pkg", "env=<bar>\narg=<test>\narg=<./pkg>\n"},
		} {
			t.Run(interpreter+"/"+test.name, func(t *testing.T) {
				stdout, stderr, status := runShellWorkerTest(t, registry, interpreter, nil, test.script, nil, invocation)
				wantStatus := 9
				if test.name == "wrapped pipeline" || test.name == "wrapped redirect" {
					wantStatus = 0
				}
				if stdout != test.want || stderr != "" || status != wantStatus {
					t.Fatalf("output/status = %q, %q, %d; want %q, empty stderr, %d", stdout, stderr, status, test.want, wantStatus)
				}
			})
		}
	}
}

func TestShellCommandRoutingWithoutExecutable(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "git"), []byte("#!/bin/sh\nprintf raw\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	invocation := newShellWorkerTestInvocation(directory, "PATH="+directory)
	for _, script := range []string{"git status", "hrun -n 1 -- git status"} {
		stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil, script, nil, invocation)
		if stdout != "raw" || stderr != "" || status != 0 {
			t.Fatalf("%s: %q, %q, %d", script, stdout, stderr, status)
		}
	}
}

func TestShellCommandRoutingPreservesArguments(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	source := "#!/bin/sh\nprintf '<%s>\\n' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(directory, "rtk"), []byte(source), 0o700); err != nil {
		t.Fatal(err)
	}
	value := "a 'quote' \"double\" $(touch never) `touch never`;\n*"
	script := "git log -- " + shellQuoteArgument(value)
	stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil, script, nil,
		newShellWorkerTestInvocation(directory, "PATH="+directory))
	want := fmt.Sprintf("<git>\n<log>\n<-->\n<%s>\n", value)
	if stdout != want || stderr != "" || status != 0 {
		t.Fatalf("argv not preserved: %q, %q, %d; want %q", stdout, stderr, status, want)
	}
	if _, err := os.Stat(filepath.Join(directory, "never")); !os.IsNotExist(err) {
		t.Fatalf("argument evaluated as shell source: %v", err)
	}
}

func TestShellCommandRoutingTracksPathAndPreservesReaders(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	rawDirectory := t.TempDir()
	for _, dir := range []string{directory, rawDirectory} {
		if err := os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\nprintf raw\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(directory, "rtk"), []byte("#!/bin/sh\nprintf routed\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "source.txt"), []byte("verified\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	invocation := newShellWorkerTestInvocation(directory, "PATH="+directory)
	script := "git status; PATH=" + shellQuoteArgument(rawDirectory) + " git status"
	stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil, script, nil, invocation)
	if stdout != "routedraw" || stderr != "" || status != 0 {
		t.Fatalf("PATH change: %q, %q, %d", stdout, stderr, status)
	}
	stdout, stderr, status = runShellWorkerTest(t, registry, "bash", nil, "hcat source.txt", nil, invocation)
	if status != 0 || stderr != "" || stdout == "" || stdout == "routed" {
		t.Fatalf("private reader was routed: %q, %q, %d", stdout, stderr, status)
	}
}

func TestShellCommandRoutingInstalledRTK(t *testing.T) {
	rtkPath, err := exec.LookPath("rtk")
	if err != nil {
		t.Skip("RTK is not installed")
	}
	t.Parallel()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "sample.txt"), []byte("sample\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	expected := exec.CommandContext(t.Context(), rtkPath, "ls", "-1", directory)
	want, err := expected.Output()
	if err != nil {
		t.Fatalf("installed RTK ls: %v", err)
	}
	stdout, stderr, status := runShellWorkerTest(t, sharedProxyTestRegistry(t), "bash", nil,
		"ls -1 "+shellQuoteArgument(directory), nil, newShellWorkerTestInvocation(directory))
	if stdout != string(want) || stderr != "" || status != 0 {
		t.Fatalf("installed RTK: %q, %q, %d; want %q", stdout, stderr, status, want)
	}
}

func TestShellCommandRoutingCandidatesDoNotBypassWorker(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	for _, script := range []string{
		"FOO=bar go test ./pkg",
		"timeout 120 go test ./pkg",
		"env FOO=bar go test ./pkg",
		"nice -n 5 go test ./pkg",
	} {
		if command, direct := registry.directBashExecCommand([]string{"bash", script}); direct {
			t.Errorf("directBashExecCommand(%q) bypassed worker as %q", script, command)
		}
	}
}
