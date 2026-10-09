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

func TestNativeRuntimeShellCapturesEffectsWithoutReplayingHistory(t *testing.T) {
	owner, store, binding := nativeObservationFixture(t)
	u, client := runtimeShellUI(t)
	u.session.cwd, u.thread = binding.Workspace, binding.Session
	u.attachRuntimeObservation(&ObservationService{owner: owner})
	for i, code := range []int{0, 7} {
		u.draft = "!printf saved > saved.txt"
		runtimeKeys(t, u, "\r")
		command := client.commands[len(client.commands)-1]
		call := u.runtime.shellCapture
		if call == nil || owner.pendingCount.Load() != 1 {
			t.Fatal("local submission did not open its native observation")
		}
		runtimeShellReceipt(t, u, "shell_started", command, "", nil)
		nativeObservationWrite(t, filepath.Join(binding.Workspace, "saved.txt"), fmt.Sprintf("saved-%d\n", i))
		runtimeShellReceipt(t, u, "shell_done", command, "native output", &code)
		key := observationKey(call.call) + "/after"
		history, found, err := store.lookup(t.Context(), binding.Workspace, key)
		if err != nil || !found || history.ChangeID == "" || len(history.ReviewFiles) != 1 || owner.pendingCount.Load() != 0 {
			t.Fatalf("native shell capture: %+v, %v, %v", history, found, err)
		}
		if (history.ExecOutcome.Status == "failed") != (code != 0) {
			t.Fatal("capture changed native command outcome")
		}
		runtimeEvidenceEvent(t, u, session.Event{Kind: "shell_done", Historical: true,
			Shell: &session.ShellResult{ShellCommand: command, Output: "native output", ExitCode: &code, Retained: true}})
		fresh, err := openMekugiReplayStore(store.directory)
		if err != nil {
			t.Fatal(err)
		}
		saved, found, err := fresh.lookup(t.Context(), binding.Workspace, key)
		if err != nil || !found || saved.ChangeID != history.ChangeID || owner.pendingCount.Load() != 0 {
			t.Fatal("native shell history replay changed retained effects")
		}
	}
	client.err = errors.New("not sent")
	u.draft = "!printf forbidden > never.txt"
	runtimeKeys(t, u, "\r")
	if u.runtime.shellCapture != nil || owner.pendingCount.Load() != 0 || u.draft == "" {
		t.Fatal("failed native submission kept an observation or lost its draft")
	}
}

func TestNativeRuntimeShellExcludesConcurrentToolEffects(t *testing.T) {
	for _, bound := range []bool{true, false} {
		t.Run(fmt.Sprint(bound), func(t *testing.T) {
			binding := ObservationBinding{Runtime: "claude", Workspace: t.TempDir(), Session: "native-session"}
			store, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			owner, err := newNativeObservationOwner(t.Context(), store, binding.Runtime, binding.Workspace)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(owner.close)
			u, client := runtimeShellUI(t)
			u.session.cwd = binding.Workspace
			u.thread = ""
			if bound {
				if err := owner.bind(t.Context(), binding); err != nil {
					t.Fatal(err)
				}
				u.thread = binding.Session
			}
			u.attachRuntimeObservation(&ObservationService{owner: owner})
			u.draft = "!printf shell > shell.txt"
			runtimeKeys(t, u, "\r")
			capture := u.runtime.shellCapture
			if capture == nil {
				t.Fatal("shell capture unavailable")
			}
			if err := owner.bind(t.Context(), binding); err != nil {
				t.Fatal(err)
			}
			other := ObservationCall{Binding: binding, ID: "other-tool", Tool: "Write", Input: `{}`, Paths: []string{filepath.Join(binding.Workspace, "other.txt")}}
			if err := owner.before(t.Context(), other); err != nil {
				t.Fatal(err)
			}
			nativeObservationWrite(t, other.Paths[0], "other tool\n")
			if _, err := owner.after(t.Context(), other, ObservationTerminal{Status: "completed"}); err != nil {
				t.Fatal(err)
			}
			command := client.commands[0]
			command.SessionID = binding.Session
			runtimeShellReceipt(t, u, "shell_started", command, "", nil)
			nativeObservationWrite(t, filepath.Join(binding.Workspace, "shell.txt"), "shell\n")
			runtimeShellReceipt(t, u, "shell_done", command, "native output", new(0))
			history := nativeObservationHistory(t, store, capture.call, "after")
			if len(history.ReviewFiles) != 1 || history.ReviewFiles[0].AfterPath != filepath.Join(binding.Workspace, "shell.txt") {
				t.Fatalf("shell captured another tool effect: %#v", history.ReviewFiles)
			}
		})
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
