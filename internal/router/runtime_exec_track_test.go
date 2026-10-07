package router

import (
	"bytes"
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
)

func TestClaudeExecTrackMatchesNativeWrapperEffects(t *testing.T) {
	t.Parallel()
	helper, err := execTrackHelper()
	if err != nil {
		t.Fatal(err)
	}
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
		t.Run(fmt.Sprint(i), func(t *testing.T) {
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
