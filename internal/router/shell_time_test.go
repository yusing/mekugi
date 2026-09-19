package router

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestShellTimeMeasuresCPU(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("time"); err != nil {
		t.Skip("external time unavailable")
	}
	registry := sharedProxyTestRegistry(t)
	stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
		`time -p sh -c 'i=0; while [ "$i" -lt 100000 ]; do i=$((i+1)); done; printf done'`, nil)
	if stdout != "done" || status != 0 {
		t.Fatalf("time = (%q, %q, %d)", stdout, stderr, status)
	}
	var cpu float64
	for _, line := range strings.Split(stderr, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && (fields[0] == "user" || fields[0] == "sys") {
			value, err := strconv.ParseFloat(fields[1], 64)
			if err != nil {
				t.Fatal(err)
			}
			cpu += value
		}
	}
	if cpu <= 0 {
		t.Fatalf("expected measured CPU time, got %q", stderr)
	}
}

func TestShellTimeUsesExternalUtility(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "time"), []byte("#!/bin/sh\nprintf '<%s>\\n' \"$@\"\nprintf 'var=%s\\n' \"$TIME_TEST_VALUE\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	invocation := newShellWorkerTestInvocation(directory, "PATH="+directory)
	for _, tc := range []struct{ script, want string }{
		{`value='a b'; time -p example "$value"`, "<-p>\n<example>\n<a b>\nvar=\n"},
		{`time -f '%U %S' example`, "<-f>\n<%U %S>\n<example>\nvar=\n"},
		{`time TIME_TEST_VALUE=present example`, "<example>\nvar=present\n"},
		{`command() { printf wrong; }; time example`, "<example>\nvar=\n"},
		{`value=$(time example); printf '%s\n' "$value"`, "<example>\nvar=\n"},
	} {
		t.Run(tc.script, func(t *testing.T) {
			stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil, tc.script, nil, invocation)
			if stdout != tc.want || stderr != "" || status != 0 {
				t.Fatalf("time = (%q, %q, %d), want %q", stdout, stderr, status, tc.want)
			}
		})
	}
}

func TestShellTimeRejectsCompoundCommandsBeforeExecution(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	for _, script := range []string{
		"printf should-not-run; time { printf inner; }",
		"time printf first | cat",
		"time (printf subshell)",
	} {
		stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil, script, nil)
		if stdout != "" || !strings.Contains(stderr, "explicit interpreter") || status != 2 {
			t.Fatalf("%s = (%q, %q, %d)", script, stdout, stderr, status)
		}
	}
}

func TestShellTimeRedirections(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
		`time -p sh -c 'printf data' >output 2>timing; cat output`, nil,
		newShellWorkerTestInvocation(directory))
	timing, err := os.ReadFile(filepath.Join(directory, "timing"))
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "data" || strings.Contains(stderr, "real") || status != 0 || !strings.Contains(string(timing), "real") {
		t.Fatalf("time = (%q, %q, %d), timing=%q", stdout, stderr, status, timing)
	}
}

func TestShellTimeDynamicCodeUsesExplicitExternalCommand(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "timed.sh"), []byte("command time -p sh -c 'printf timed'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, script := range []string{
		`eval "command time -p sh -c 'printf timed'"`,
		`source ./timed.sh`,
		`trap "command time -p sh -c 'printf timed'" EXIT; exit 0`,
	} {
		stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil, script, nil,
			newShellWorkerTestInvocation(directory))
		if stdout != "timed" || !strings.Contains(stderr, "user") || status != 0 {
			t.Fatalf("%s = (%q, %q, %d)", script, stdout, stderr, status)
		}
	}
}
