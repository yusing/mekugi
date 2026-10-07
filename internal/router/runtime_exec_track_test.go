package router

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/execsegment"
	"github.com/yusing/mekugi/internal/session"
	"github.com/yusing/mekugi/internal/shellsyntax"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"github.com/yusing/mekugi/internal/uisnapshot"
	"golang.org/x/sys/unix"
)

func TestClaudeExecTrackGuardFallbackKeepsOrdinaryWait(t *testing.T) {
	t.Parallel()
	helper, err := execTrackHelper()
	if err != nil {
		t.Fatal(err)
	}
	for _, script := range []string{
		"git status; wait; printf 'done\\n'; printf once >> effects",
		"printf 'status\\n'; wait; printf 'done\\n'; printf once >> effects",
	} {
		t.Run(script, func(t *testing.T) {
			shell := newExecTrackShell(t)
			socket, directory := ExecTrackPaths(filepath.Join(shell.root, "bin"))
			guard := filepath.Join(shell.root, "guard")
			if err := os.Mkdir(guard, 0700); err != nil {
				t.Fatal(err)
			}
			tracker, startup := filepath.Join(shell.root, "claude-track"), filepath.Join(shell.root, "claude-env")
			for path, source := range map[string]string{
				tracker:                          execsegment.ClaudeGuardTracker(helper, socket, directory, guard),
				startup:                          execsegment.ClaudeGuardHook(tracker),
				filepath.Join(shell.root, "git"): "#!" + execTrackShellExecutable(t, "bash") + "\n[ \"$*\" = status ] || exit 9\nprintf git >> git-effects\nprintf 'status\\n'\n",
			} {
				if err := os.WriteFile(path, []byte(source), 0700); err != nil {
					t.Fatal(err)
				}
			}
			snapshot, cwd := filepath.Join(shell.root, "snapshot"), filepath.Join(shell.root, "claude-test-cwd")
			if err := os.WriteFile(snapshot, nil, 0600); err != nil {
				t.Fatal(err)
			}
			wrapper := "source " + shellsyntax.Quote(snapshot) + " 2>/dev/null || true && eval " + shellsyntax.Quote(script) + " < /dev/null && pwd -P >| " + shellsyntax.Quote(cwd)
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, execTrackShellExecutable(t, "bash"), "--noprofile", "--norc", "-c", wrapper)
			command.Dir = shell.root
			command.Env = []string{"PATH=" + shell.root + ":" + execTrackPath(), "HOME=" + shell.home, "BASH_ENV=" + startup}
			command.SysProcAttr = &unix.SysProcAttr{Setpgid: true}
			// The old startup trap leaves the helper waiting on the shell's
			// coprocess pipe. Cancel the whole fixture group, not only Bash.
			command.Cancel = func() error { return unix.Kill(-command.Process.Pid, unix.SIGKILL) }
			command.WaitDelay = time.Second
			var stdout, stderr bytes.Buffer
			command.Stdout, command.Stderr = &stdout, &stderr
			if err := command.Run(); err != nil {
				t.Fatalf("native wait failed: %v (context %v), stdout %q, stderr %q", err, ctx.Err(), stdout.String(), stderr.String())
			}
			if stdout.String() != "status\ndone\n" || stderr.Len() != 0 {
				t.Fatalf("native output changed: stdout %q, stderr %q", stdout.String(), stderr.String())
			}
			for name, want := range map[string]string{"effects": "once", "claude-test-cwd": shell.root + "\n", "git-effects": ""} {
				if name == "git-effects" && strings.HasPrefix(script, "git ") {
					want = "git"
				}
				data, err := os.ReadFile(filepath.Join(shell.root, name))
				if string(data) != want || err != nil && !(want == "" && os.IsNotExist(err)) {
					t.Fatalf("%s = %q, %v; want %q", name, data, err, want)
				}
			}
			shell.hub.mu.Lock()
			defer shell.hub.mu.Unlock()
			if len(shell.hub.tracks) != 0 || len(shell.hub.started) != 0 {
				t.Fatal("unclaimed native execution created guessed segment tracking")
			}
		})
	}
}

func TestClaudeExecTrackMatchesNativeWrapperEffects(t *testing.T) {
	t.Parallel()
	helper, err := execTrackHelper()
	if err != nil {
		t.Fatal(err)
	}
	for _, guarded := range []bool{false, true} {
		for i, script := range []string{
			"printf 'first\\n'; printf 'second\\n' >&2; printf once >> effects",
			"printf 'first\\n'; false && printf never; printf 'recovered\\n'",
			"printf 'first\\n'; exit 7; printf never",
			"set -e; printf 'first\\n'; false; printf 'native eval continuation\\n'",
			"printf 'first\\n'; printf '%s' \"${MISSING_NATIVE:?native failure}\"; printf never",
			"cat <<'EOF'; snapshot_echo; cd sub\nheredoc body\nEOF\n",
			"printf 'single\\n'",
			"false",
		} {
			t.Run(fmt.Sprintf("guard=%t/%d", guarded, i), func(t *testing.T) {
				service, binding, _ := observationHTTPFixture(t)
				prior := filepath.Join(t.TempDir(), "prior-env")
				oldTracker := filepath.Join(t.TempDir(), "old-observer")
				oldSocket, oldDirectory := ExecTrackPaths(filepath.Join(t.TempDir(), "bin"))
				if err := os.WriteFile(oldTracker, []byte(execsegment.Tracker(helper, oldSocket, oldDirectory)), 0600); err != nil {
					t.Fatal(err)
				}
				previous := "export PRIOR_NATIVE_ENV=kept\n" + execsegment.Hook(oldTracker)
				if err := os.WriteFile(prior, []byte(previous), 0600); err != nil {
					t.Fatal(err)
				}
				startup, err := service.PrepareCommandTracking(t.Context(), helper, prior)
				if err != nil {
					t.Fatal(err)
				}
				if guarded {
					if err := service.PrepareVCSGuard(t.Context(), helper); err != nil {
						t.Fatal(err)
					}
				}
				snapshot := filepath.Join(binding.Workspace, "snapshot")
				if err := os.WriteFile(snapshot, []byte("snapshot_echo() { printf 'snapshot %s\\n' \"$PRIOR_NATIVE_ENV\"; }\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(binding.Workspace, "sub"), 0700); err != nil {
					t.Fatal(err)
				}
				cwd := filepath.Join(t.TempDir(), "claude-ab12-cwd")
				wrapper := "source " + shellsyntax.Quote(snapshot) + " 2>/dev/null || true && export NATIVE_SETUP=kept && eval " + shellsyntax.Quote(script) + " < /dev/null && pwd -P >| " + shellsyntax.Quote(cwd)
				run := func(environment []string) (execTrackRun, string) {
					command := exec.Command(execTrackShellExecutable(t, "bash"), "-lc", wrapper)
					command.Dir, command.Env = binding.Workspace, environment
					var stdout, stderr bytes.Buffer
					command.Stdout, command.Stderr = &stdout, &stderr
					code := 0
					if err := command.Run(); err != nil {
						if exit, ok := err.(*exec.ExitError); ok {
							code = exit.ExitCode()
						} else {
							t.Fatal(err)
						}
					}
					directory, _ := os.ReadFile(cwd)
					if err := os.Remove(cwd); err != nil && !os.IsNotExist(err) {
						t.Fatal(err)
					}
					return execTrackRun{stdout.String(), stderr.String(), code}, string(directory)
				}
				env := []string{"PATH=" + execTrackPath(), "HOME=" + t.TempDir(), "BASH_ENV=" + prior}
				plain, plainCwd := run(env)
				if err := service.owner.bind(t.Context(), binding); err != nil {
					t.Fatal(err)
				}
				input, _ := json.Marshal(map[string]string{"command": script})
				call := ObservationCall{Binding: binding, ID: "native-call", Tool: "Bash", Input: string(input), Command: script}
				if err := service.owner.before(t.Context(), call); err != nil {
					t.Fatal(err)
				}
				env = slices.DeleteFunc(env, func(value string) bool { return strings.HasPrefix(value, "BASH_ENV=") })
				// An unrelated inherited Codex identity must not restrict a native claim.
				env = append(env, "BASH_ENV="+startup, "CODEX_THREAD_ID=unrelated")
				tracked, trackedCwd := run(env)
				if tracked != plain || trackedCwd != plainCwd {
					t.Fatalf("changed native execution: tracked %+v cwd %q, plain %+v cwd %q", tracked, trackedCwd, plain, plainCwd)
				}
				key := [3]string{binding.Session, binding.Agent, call.ID}
				shell := &execTrackShell{hub: service.owner.execTrack}
				view := shell.awaitView(t, key)
				parts, _ := execsegment.Split(script)
				if !view.complete || view.code != tracked.code || len(view.segments) != len(parts) || len(parts) > 1 && !view.output {
					t.Fatalf("missing shell evidence: %+v", view)
				}
				if i == 0 {
					if !slices.Equal(view.segments[0].tail, []string{"first"}) || !slices.Equal(view.segments[1].tail, []string{"second"}) {
						t.Fatalf("misattributed output: %+v", view.segments)
					}
					effects, _ := os.ReadFile(filepath.Join(binding.Workspace, "effects"))
					if string(effects) != "onceonce" {
						t.Fatalf("execution count changed: %q", effects)
					}
				}
				if i == 1 && !view.segments[2].skipped {
					t.Fatal("short circuit did not mark skipped segment")
				}
				if i == 2 && (view.segments[1].exit != 7 || !view.segments[2].skipped) {
					t.Fatal("lost actual exit/skip evidence")
				}
			})
		}
	}
}

func TestClaudeExecTrackGuardRejectsLostTracker(t *testing.T) {
	t.Parallel()
	helper, err := execTrackHelper()
	if err != nil {
		t.Fatal(err)
	}
	service, binding, _ := observationHTTPFixture(t)
	startup, err := service.PrepareCommandTracking(t.Context(), helper, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.PrepareVCSGuard(t.Context(), helper); err != nil {
		t.Fatal(err)
	}
	tracker := filepath.Join(filepath.Dir(startup), "exec-track.bash")
	data, err := os.ReadFile(tracker)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.WriteFile(tracker, data, 0600) })
	if err := os.Remove(tracker); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(binding.Workspace, "snapshot")
	nativeObservationWrite(t, snapshot, "")
	marker := filepath.Join(binding.Workspace, "effects")
	wrapper := "source " + shellsyntax.Quote(snapshot) + " 2>/dev/null || true && eval " + shellsyntax.Quote("printf unsafe >> "+shellsyntax.Quote(marker)) + " < /dev/null && pwd -P >| " + shellsyntax.Quote(filepath.Join(binding.Workspace, "claude-test-cwd"))
	command := exec.Command(execTrackShellExecutable(t, "bash"), "--noprofile", "--norc", "-c", wrapper)
	command.Env = []string{"PATH=" + execTrackPath(), "BASH_ENV=" + startup}
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "native VCS guard unavailable") {
		t.Fatalf("lost tracker did not reject execution: %v, %q", err, output)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("lost tracker ran effects: %v", err)
	}
}

func TestClaudeExecTrackGuardRejectsDeliveryFailure(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"missing-helper", "report-handshake", "replacement-script"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			helper := filepath.Join(root, "helper")
			if failure != "missing-helper" {
				source := "#!/bin/sh\nexit 1\n"
				if failure == "replacement-script" {
					source = "#!/bin/sh\nprintf 'relay %s\\n' " + shellsyntax.Quote(filepath.Join(root, "absent-report")) + "\n"
				}
				nativeObservationWrite(t, helper, source)
				if err := os.Chmod(helper, 0700); err != nil {
					t.Fatal(err)
				}
			}
			tracker, startup := filepath.Join(root, "tracker"), filepath.Join(root, "startup")
			nativeObservationWrite(t, tracker, execsegment.ClaudeGuardTracker(helper, filepath.Join(root, "requests"), filepath.Join(root, "reports"), filepath.Join(root, "guard")))
			nativeObservationWrite(t, startup, execsegment.ClaudeGuardHook(tracker))
			marker := filepath.Join(root, "effects")
			script := "printf unsafe >> " + shellsyntax.Quote(marker)
			wrapper := "eval " + shellsyntax.Quote(script) + " < /dev/null && pwd -P >| " + shellsyntax.Quote(filepath.Join(root, "claude-test-cwd"))
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, execTrackShellExecutable(t, "bash"), "--noprofile", "--norc", "-c", wrapper)
			command.Env = []string{"PATH=" + execTrackPath(), "BASH_ENV=" + startup}
			output, err := command.CombinedOutput()
			if ctx.Err() != nil || err == nil || !strings.Contains(string(output), "native VCS guard unavailable") {
				t.Fatalf("delivery failure did not reject execution: %v, %q", err, output)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("delivery failure ran effects: %v", err)
			}
		})
	}
}

func TestClaudeExecTrackRetiresUnmatchedNativeClaims(t *testing.T) {
	t.Parallel()
	shell := newExecTrackShell(t)
	key := [3]string{"session", "agent", "call"}
	shell.hub.startScript(key, "printf one; printf two")
	shell.hub.nativeCompleted(key)
	shell.hub.mu.Lock()
	defer shell.hub.mu.Unlock()
	if len(shell.hub.started) != 0 {
		t.Fatal("completed native command leaked an unmatched claim")
	}
}

func runtimeSegmentUI(t *testing.T) (*appServerUI, *execTrack) {
	t.Helper()
	u, _ := runtimeTestUI(t)
	script := "printf 'first\\n'; false && printf 'never\\n'"
	input, _ := json.Marshal(map[string]string{"command": script})
	runtimeEvidenceEvent(t, u, session.Event{Kind: "tool", ID: "command", Role: "Bash", Text: string(input)})
	parts, _ := execsegment.Split(script)
	track := &execTrack{script: script}
	for _, part := range parts {
		track.segments = append(track.segments, execTrackSegment{source: part.Source})
	}
	u.execTrack = &execTrackHub{tracks: map[[3]string]*execTrack{{u.thread, "", "command"}: track}, changed: make(chan struct{})}
	start := u.now().Add(-time.Second)
	track.apply(execsegment.Message{Type: execsegment.Begin, Index: 0, Timing: execsegment.Timing{Started: start}})
	track.apply(execsegment.Message{Type: execsegment.Output, Index: 0, Data: "first\n"})
	track.apply(execsegment.Message{Type: execsegment.End, Index: 0, Code: new(0), Timing: execsegment.Timing{Started: start, Ended: start.Add(20 * time.Millisecond), ElapsedNS: int64(20 * time.Millisecond)}})
	track.apply(execsegment.Message{Type: execsegment.Begin, Index: 1, Timing: execsegment.Timing{Started: start.Add(20 * time.Millisecond)}})
	u.flushRuntimeCommandSegments()
	// Live invocations use the shared operation cadence. Advance its clock,
	// as the real terminal tick does, before inspecting measured segment rows.
	u.view.pace(u.now().Add(time.Second))
	u.agents.pace(u.now().Add(time.Second))
	return u, track
}

func TestUISnapshotNativeRuntimeCommandSegments(t *testing.T) {
	for _, width := range []int{48, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			u, track := runtimeSegmentUI(t)
			uisnapshot.Assert(t, filepath.Join("testdata", "snapshots", fmt.Sprintf("native-runtime-segments-live-%d.txt", width)), runtimeFrame(t, u, width, 32))
			track.apply(execsegment.Message{Type: execsegment.End, Index: 1, Code: new(1)})
			track.apply(execsegment.Message{Type: execsegment.Done, Code: new(1)})
			track.ended = true
			runtimeEvidenceEvent(t, u, session.Event{Kind: "tool_result", ID: "command", Text: "first\n", Failed: true})
			u.flushRuntimeCommandSegments()
			uisnapshot.Assert(t, filepath.Join("testdata", "snapshots", fmt.Sprintf("native-runtime-segments-final-%d.txt", width)), runtimeFrame(t, u, width, 32))
		})
	}
}

func TestClaudeRuntimeSegmentsKeepDialogAndNativeSettlement(t *testing.T) {
	u, track := runtimeSegmentUI(t)
	output := u.view.entries[0].native.segments[0].output
	runtimeParityClick(t, u, u.view, 120, func(block activityui.Block) bool { return block.Output == output })
	if u.shell.output == nil {
		t.Fatal("shared segment output did not open")
	}
	track.apply(execsegment.Message{Type: execsegment.Output, Index: 1, Data: "partial\n"})
	u.flushRuntimeCommandSegments()
	if u.shell.output == nil || u.view.entries[0].native.segments[0].output != output {
		t.Fatal("segment update lost open dialog identity")
	}
	track.apply(execsegment.Message{Type: execsegment.End, Index: 1, Code: new(1)})
	track.apply(execsegment.Message{Type: execsegment.Done, Code: new(1)})
	track.ended = true
	u.flushRuntimeCommandSegments()
	if !u.view.entries[0].native.running || len(u.view.entries[0].native.segments) != 2 {
		t.Fatal("shell report invented native completion")
	}
	runtimeEvidenceEvent(t, u, session.Event{Kind: "tool_result", ID: "command", Text: "native aggregate\n", Failed: true})
	u.flushRuntimeCommandSegments()
	parts := u.view.entries[0].native.segments
	if len(parts) != 3 || parts[1].exit != 1 || !parts[2].skipped || parts[2].output != nil {
		t.Fatalf("incorrect terminal segment evidence: %+v", parts)
	}
}

func TestClaudeRuntimeSegmentsFallBackOnDisagreement(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(fmt.Sprint(complete), func(t *testing.T) {
			u, track := runtimeSegmentUI(t)
			track.ended, track.done, track.code, track.dirty = true, complete, 1, true
			runtimeEvidenceEvent(t, u, session.Event{Kind: "tool_result", ID: "command", Text: "authoritative native aggregate\n"})
			u.flushRuntimeCommandSegments()
			for _, view := range []*liveActivityView{u.view, u.agents} {
				if len(view.entries[0].native.segments) != 0 || strings.Join(view.entries[0].native.output.View().Lines, "\n") != "authoritative native aggregate" {
					t.Fatal("incomplete or conflicting report displaced native evidence")
				}
			}
		})
	}
}
