package main

import (
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strconv"
	"syscall"
	"testing"
)

func TestGPGExitSignal(t *testing.T) {
	if value := os.Getenv("MEKUGI_TEST_GPG_EXIT_SIGNAL"); value != "" {
		// Match the forwarding handlers installed by startGPG.
		signal.Notify(make(chan os.Signal, 1), syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM, syscall.SIGHUP)
		cmd := exec.Command("/bin/sh", "-c", `exec /bin/kill -"$1" "$$"`, "sh", value)
		if err := cmd.Run(); err == nil || cmd.ProcessState == nil {
			t.Fatal("signal fixture did not terminate")
		}
		os.Exit(gpgExit(cmd.ProcessState))
	}
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGPIPE, syscall.SIGKILL} {
		t.Run(sig.String(), func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestGPGExitSignal$")
			cmd.Env = append(os.Environ(), "MEKUGI_TEST_GPG_EXIT_SIGNAL="+strconv.Itoa(int(sig)), "PATH=")
			if err := cmd.Run(); err == nil || cmd.ProcessState == nil {
				t.Fatal("GPG exit adapter did not terminate")
			}
			status := cmd.ProcessState.Sys().(syscall.WaitStatus)
			if !status.Signaled() || status.Signal() != sig {
				t.Fatalf("GPG exit status = %v, want signal %v", status, sig)
			}
		})
	}
}

func TestGPGArgumentOwnership(t *testing.T) {
	options := map[string]int{"--pinentry-mode": 2, "--passphrase-fd": 1, "--passphrase-file": 2, "--command-fd": 1, "--command-file": 2, "--status-file": 2, "--options": 2, "--status-fd": 1, "--detach-sign": 128, "--output": 2, "-o": 2, "--batch": 0, "--store": 128}
	for _, args := range [][]string{
		{"--pinentry-mode=cancel", "-s"}, {"--pinentry", "ask", "-s"},
		{"--passphrase-fd", "7", "-d"}, {"--passphrase-file=private", "-d"},
		{"--command-fd", "3", "--edit-key", "key"}, {"--command-file=input"},
		{"--status-file=status", "-s"}, {"--status-f", "status", "-s"},
		{"--options", "custom.conf", "-s"}, {"--status-fd"}, {"--status-fd=invalid"},
	} {
		got, _, interactive := gpgArguments(args, options)
		if interactive || !slices.Equal(got, args) {
			t.Fatal("caller-owned GPG options were replaced")
		}
	}
	for _, option := range [][]string{{"--status-fd", "2"}, {"--status-fd=2"}} {
		args := append(option, "--detach-sign", "--", "--pinentry-mode")
		got, status, interactive := gpgArguments(args, options)
		if !interactive || status != 2 || !slices.Equal(got, []string{"--detach-sign", "--", "--pinentry-mode"}) {
			t.Fatal("status routing or positional arguments changed")
		}
	}
	for _, operand := range []string{"--status-fd=2", "--command-fd", "--passphrase-fd"} {
		for _, flag := range []string{"--output", "-o"} {
			args := []string{"--batch", flag, operand, "--store", "input"}
			got, status, interactive := gpgArguments(args, options)
			if !interactive || status != -1 || !slices.Equal(got, args) {
				t.Fatal("GPG option operand was interpreted as an option")
			}
		}
	}
}
