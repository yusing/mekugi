package main

import (
	"slices"
	"testing"
)

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
