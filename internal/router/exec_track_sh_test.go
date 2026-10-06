package router

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/execsegment"
	"github.com/yusing/mekugi/internal/shellsyntax"
)

func TestExecTrackDashMissingHelperRunsOriginal(t *testing.T) {
	t.Parallel()
	shell := newExecTrackShell(t)
	helper, err := execTrackHelper()
	if err != nil {
		t.Fatal(err)
	}
	// The first helper call succeeds but removes its own entrypoint before
	// dash starts the reporting helper. Keep the real helper's direct parent.
	entrypoint := filepath.Join(shell.root, "vanishing-helper")
	if err := os.WriteFile(entrypoint, []byte("#!/bin/sh\nrm -- \"$0\"\nexec "+shellsyntax.Quote(helper)+" \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	tracker := filepath.Join(shell.root, "exec-track.sh")
	if err := os.WriteFile(tracker, []byte(execsegment.ShTracker(entrypoint, shell.hub.requests.Name(), shell.hub.directory)), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "printf 'ORIGINAL\\n'; printf 'STDERR\\n' >&2; false"
	command := execsegment.ShScript(tracker, script)
	shell.hub.start([3]string{"thread", "turn", "missing-helper"}, "/bin/sh -c "+shellsyntax.Quote(command))
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, execTrackShellExecutable(t, "sh"), "-c", command)
	cmd.Env = shell.env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if _, statErr := os.Stat(entrypoint); !os.IsNotExist(statErr) {
		t.Fatalf("helper disappearance was not exercised: %v", statErr)
	}
	exit, ok := errors.AsType[*exec.ExitError](err)
	if ctx.Err() != nil || !ok || exit.ExitCode() != 1 || string(output) != "ORIGINAL\n" || stderr.String() != "STDERR\n" {
		t.Fatalf("helper disappearance stalled or changed command: stdout %q, stderr %q, exit %v, deadline %v", output, stderr.String(), err, ctx.Err())
	}
}

func TestExecTrackDashSegments(t *testing.T) {
	t.Parallel()
	shell := newExecTrackShell(t)
	helper, err := execTrackHelper()
	if err != nil {
		t.Fatal(err)
	}
	channel, directory := ExecTrackPaths(filepath.Join(shell.root, "bin"))
	tracker := filepath.Join(shell.root, "exec-track.sh")
	if err := os.WriteFile(tracker, []byte(execsegment.ShTracker(helper, channel, directory)), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, script := range []string{
		"printf first; sleep .03; printf second >&2; false && printf never; printf last",
		"printf '%s' $?; false; printf '%s' $?; cd /; pwd",
		"set -e; false || printf recovered; ! true; printf never",
		"cat <<'EOF'\nheredoc\nEOF\nprintf tail",
		"printf first; exit 7; printf never",
		"printf first; echo ${missing:?fatal}; printf never",
		"printf single",
		"printf '%s' $?",
		"exit",
	} {
		t.Run(script, func(t *testing.T) {
			instrumented := execsegment.ShScript(tracker, script)
			if original := execsegment.ShOriginal(instrumented); original != script {
				t.Fatalf("envelope lost script: %q", original)
			}
			key := [3]string{"thread", "turn", script}
			shell.hub.start(key, "/bin/sh -c "+shellsyntax.Quote(instrumented))
			plain := runShell(t, shell.env, "sh", "-c", script)
			tracked := runShell(t, shell.env, "sh", "-c", instrumented)
			if tracked != plain {
				t.Fatalf("tracked=%+v, plain=%+v", tracked, plain)
			}
			view := shell.awaitView(t, key)
			segments, _ := execsegment.Split(script)
			if !view.ended || !view.complete || view.code != plain.code || len(view.segments) != len(segments) {
				t.Fatalf("incomplete report: %+v", view)
			}
			var output strings.Builder
			shell.hub.mu.Lock()
			for _, segment := range shell.hub.tracks[key].segments {
				output.Write(segment.fresh)
			}
			shell.hub.mu.Unlock()
			for i, segment := range view.segments {
				if segment.source != segments[i].Source || !segment.skipped && segment.timing.ElapsedNS <= 0 {
					t.Fatalf("segment identity/timing: %+v", segment)
				}
			}
			if len(segments) > 1 && output.String() != plain.stdout+plain.stderr {
				// Mixed streams are ordered by arrival, not grouped by stream.
				if script != "printf first; sleep .03; printf second >&2; false && printf never; printf last" || output.String() != "firstsecondlast" {
					t.Fatalf("segment output=%q, host=%q/%q", output.String(), plain.stdout, plain.stderr)
				}
			}
		})
	}
}

func TestExecTrackShEnvelopeKeepsBash(t *testing.T) {
	t.Parallel()
	shell := newExecTrackShell(t)
	script := "printf first; false; printf last"
	wrapped := execsegment.ShScript(filepath.Join(shell.root, "exec-track.sh"), script)
	key := [3]string{"thread", "turn", "bash"}
	shell.hub.start(key, "/bin/bash -c "+shellsyntax.Quote(wrapped))
	plainEnv := slices.DeleteFunc(slices.Clone(shell.env), func(entry string) bool { return strings.HasPrefix(entry, "BASH_ENV=") })
	plain := runShell(t, plainEnv, "bash", "-c", script)
	if tracked := runShell(t, shell.env, "bash", "-c", wrapped); tracked != plain {
		t.Fatalf("tracked=%+v plain=%+v", tracked, plain)
	}
	if view := shell.awaitView(t, key); !view.complete || len(view.segments) != 3 || view.segments[1].exit != 1 {
		t.Fatalf("Bash report lost: %+v", view)
	}
}
