package router

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/yusing/mekugi/internal/session"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

type runtimeShellClient struct {
	*runtimeTestClient
	commands []session.ShellCommand
	err      error
}

func (c *runtimeShellClient) RunShell(_ context.Context, command session.ShellCommand) error {
	c.commands = append(c.commands, command)
	return c.err
}
func runtimeShellUI(t *testing.T) (*appServerUI, *runtimeShellClient) {
	t.Helper()
	u, base := runtimeTestUI(t)
	c := &runtimeShellClient{runtimeTestClient: base}
	u.runtime.client = c
	return u, c
}
func runtimeShellReceipt(t *testing.T, u *appServerUI, kind string, c session.ShellCommand, output string, code *int) {
	t.Helper()
	runtimeEvidenceEvent(t, u, session.Event{Kind: kind, Shell: &session.ShellResult{ShellCommand: c, Output: output, ExitCode: code, Retained: kind == "shell_done"}})
}

func TestNativeRuntimeShellSubmissionAndIndependentSettlement(t *testing.T) {
	u, c := runtimeShellUI(t)
	u.runtime.busy = true
	runtimeKeys(t, u, "!printf '%s' '$HOME' | cat > result\t")
	command := c.commands[0]
	if command.Command != "printf '%s' '$HOME' | cat > result" || len(c.sent) != 0 || u.draft != "" || !u.sessionBusy() {
		t.Fatal("shell changed native command or submitted Main input")
	}
	runtimeShellReceipt(t, u, "shell_started", command, "", nil)
	runtimeEvidenceEvent(t, u, session.Event{Kind: "done"})
	runtimeKeys(t, u, "Follow up\r")
	if len(c.sent) != 0 || u.draft != "Follow up" || !u.sessionBusy() {
		t.Fatal("Main completion released unfinished shell history")
	}
	runtimeShellReceipt(t, u, "shell_done", command, "世界\n", new(0))
	if u.draft != "Follow up" || u.runtime.shell != nil || u.sessionBusy() || len(u.inputHistory) != 1 {
		t.Fatal("shell completion lost draft or retained input")
	}
	runtimeKeys(t, u, "\r")
	if len(c.sent) != 1 || c.sent[0] != "Follow up" {
		t.Fatal("follow-up did not use ordinary native input")
	}
}

func TestNativeRuntimeShellFailureRecoveryAndInterruption(t *testing.T) {
	u, c := runtimeShellUI(t)
	c.err = errors.New("transport unavailable")
	runtimeKeys(t, u, "!pwd\r")
	if u.draft != "!pwd" || u.runtime.shell != nil {
		t.Fatal("failed send consumed executable draft")
	}
	c.err = nil
	runtimeKeys(t, u, "\r")
	command := c.commands[len(c.commands)-1]
	u.loadDraft(composerDraft{text: "Later Main draft"})
	runtimeEvidenceEvent(t, u, session.Event{Kind: "shell_done", Failed: true, Text: "Native session not ready", Shell: &session.ShellResult{ShellCommand: command}})
	if u.draft != "Later Main draft" || len(u.inputHistory) != 1 || u.runtime.shell != nil {
		t.Fatal("rejection overwrote later prose or lost command recovery")
	}
	u.loadDraft(composerDraft{text: "!sleep 30"})
	runtimeKeys(t, u, "\r")
	command = c.commands[len(c.commands)-1]
	runtimeShellReceipt(t, u, "shell_started", command, "", nil)
	u.interruptLocked = true
	runtimeKeys(t, u, "\x03")
	if c.interrupts != 0 {
		t.Fatal("shell bypassed keyboard lock")
	}
	u.interruptLocked = false
	runtimeKeys(t, u, "\x03")
	if c.interrupts != 1 || u.runtime.shell == nil {
		t.Fatal("shell interruption claimed completion before native result")
	}
	runtimeShellReceipt(t, u, "shell_done", command, "Interrupted", new(1))
	if u.runtime.busy || u.runtime.shell != nil || len(c.sent) != 0 {
		t.Fatal("shell settlement started a model turn")
	}
}

func TestUISnapshotNativeRuntimeShell(t *testing.T) {
	for _, width := range []int{48, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			u, c := runtimeShellUI(t)
			runtimeKeys(t, u, "!printf 'Hello 世界\\n' > result.txt")
			uisnapshot.Assert(t, filepath.Join("testdata", "snapshots", fmt.Sprintf("native-runtime-shell-draft-%d.txt", width)), runtimeFrame(t, u, width, 32))
			runtimeKeys(t, u, "\r")
			command := c.commands[0]
			runtimeShellReceipt(t, u, "shell_started", command, "", nil)
			runtimeShellReceipt(t, u, "shell_done", command, "Hello 世界\nNative shell failed\n", new(7))
			finishPacing(u.view, u.agents)
			uisnapshot.Assert(t, filepath.Join("testdata", "snapshots", fmt.Sprintf("native-runtime-shell-complete-%d.txt", width)), runtimeFrame(t, u, width, 32))
		})
	}
}
