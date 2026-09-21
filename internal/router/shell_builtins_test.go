package router

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestShellBuiltinType(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	mcatType := "mcat is " + registry.frontends["mcat"] + "\n"
	mchangesType := "mchanges is " + registry.frontends["mchanges"] + "\n"
	mreadType := "mread is " + registry.frontends["mread"] + "\n"
	mrunType := "mrun is " + registry.frontends["mrun"] + "\n"
	for _, tc := range []struct {
		name, script, want string
		status             int
	}{
		{"helpers", "type mcat hpatch mread mchanges mrun journal", mcatType + "[mekugi-builtin]\n" + mreadType + mchangesType + mrunType + "[mekugi-builtin]\n", 0},
		{"mixed", "type mcat printf mread mrun", mcatType + "[mekugi-builtin]\n" + mreadType + mrunType, 0},
		{"function", "example() { :; }; type mcat example", mcatType + "example is a function\n", 0},
		{"missing", "type mcat mekugi_missing_command mread", mcatType + mreadType, 1},
		{"quoted", "type mcat '$(echo unsafe)'", mcatType, 1},
		{"plus option", "type +x printf", "", 2},
		{"custom builtin", "builtin() { printf custom; }; builtin type mcat", "custom", 0},
		{"custom command", "command() { printf custom; }; command type mcat", "custom", 0},
		{"invalid option", "type -z mcat", "", 2},
		{"option operand", "type mcat -t", mcatType, 1},
		{"custom type", "type() { printf custom; }; type mcat", "custom", 0},
		{"custom helpers", "eval() { printf wrong; }; builtin() { printf wrong; }; exit() { printf wrong; }; type mcat", mcatType, 0},
		{"custom kill", "kill() { printf custom; }; kill -0 1", "custom", 0},
		{"ordinary prefixes", "command builtin type printf", "printf is a function\n", 0},
		{"prefix positional scope", "command set -- preserved; builtin set -- final; printf '%s' \"$1\"", "final", 0},
		{"ordinary", "type printf", "[mekugi-builtin]\n", 0},
		{"substitution", "x=$(type mcat); printf '%s\\n' \"$x\"", mcatType, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil, tc.script, nil)
			if stdout != tc.want || status != tc.status {
				t.Fatalf("got (%q, %q, %d), want (%q, %d)", stdout, stderr, status, tc.want, tc.status)
			}
		})
	}
}

func TestShellBuiltinKill(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	t.Run("probe", func(t *testing.T) {
		stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil, fmt.Sprintf("kill -0 %d", os.Getpid()), nil)
		if stdout != "" || stderr != "" || status != 0 {
			t.Fatalf("kill -0 = (%q, %q, %d)", stdout, stderr, status)
		}
	})
	process := exec.CommandContext(t.Context(), "sleep", "60")
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = process.Process.Kill(); _ = process.Wait() })
	stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil, fmt.Sprintf("kill -TERM %d", process.Process.Pid), nil)
	if stdout != "" || stderr != "" || status != 0 {
		t.Fatalf("kill -TERM = (%q, %q, %d)", stdout, stderr, status)
	}
	if err := process.Wait(); err == nil {
		t.Fatal("expected terminated process")
	}
}

func TestShellBuiltinTime(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	for _, prefix := range []string{"time", "time -p"} {
		t.Run(prefix, func(t *testing.T) {
			stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil, prefix+" sh -c 'printf timed; exit 7'", nil)
			if stdout != "timed" || !strings.Contains(stderr, "user") || status != 7 {
				t.Fatalf("time = (%q, %q, %d)", stdout, stderr, status)
			}
		})
	}
}

func TestShellBuiltinKillBackgroundIDIsNotAnOSPID(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "kill"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
		"true & kill \"$!\"; wait", nil,
		newShellWorkerTestInvocation(directory, "PATH="+directory))
	if stdout != "g1\n" || stderr != "" || status != 0 {
		t.Fatalf("background ID = (%q, %q, %d)", stdout, stderr, status)
	}
}

func TestShellBuiltinExternalTime(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("time"); err != nil {
		t.Skip("external time unavailable")
	}
	registry := sharedProxyTestRegistry(t)
	stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
		"command time -p sh -c 'printf timed; exit 7'", nil)
	if stdout != "timed" || !strings.Contains(stderr, "real") || status != 7 {
		t.Fatalf("external time = (%q, %q, %d)", stdout, stderr, status)
	}
}

func TestShellBuiltinBypassRetainsNativeBehavior(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	for _, script := range []string{"command kill -l", "builtin kill -l", "command builtin kill -l"} {
		stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil, script, nil)
		if stdout != "" || !strings.Contains(stderr, "unsupported builtin") || status != 2 {
			t.Fatalf("%s = (%q, %q, %d)", script, stdout, stderr, status)
		}
	}
}
