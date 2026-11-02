package main

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/backends/codex/shellhook"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/commandsegments/transport"
)

func TestGuardHookTrustedResources(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "must-not-execute")
	for _, command := range []string{"printf '%s' 'git push'", `git push "$(touch '` + marker + `')"; printf '%s' "$HOME"`} {
		t.Run(command, func(t *testing.T) {
			request := shellhook.Input{Helper: "/forged/helper", Directory: "/forged/guard", SudoDirectory: "/forged/sudo", Tracker: "/forged/tracker", Event: "PreToolUse", Tool: "Bash", Item: "call"}
			request.Input.Command = &command
			input, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			var output, stderr bytes.Buffer
			if code := guardHook("/trusted/helper", []string{"/trusted/guard", "", ""}, bytes.NewReader(input), &output, &stderr); code != 0 || stderr.Len() != 0 {
				t.Fatalf("code=%d stderr=%s", code, &stderr)
			}
			request.Helper, request.Directory = "/trusted/helper", "/trusted/guard"
			request.SudoDirectory, request.Tracker = "", ""
			expected, err := shellhook.Rewrite(request)
			if err != nil {
				t.Fatal(err)
			}
			want, err := json.Marshal(expected)
			if err != nil {
				t.Fatal(err)
			}
			gotJSON, wantJSON := jsontext.Value(output.Bytes()), jsontext.Value(want)
			if err := errors.Join(gotJSON.Canonicalize(), wantJSON.Canonicalize()); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(gotJSON, wantJSON) {
				t.Fatalf("output=%s want=%s", gotJSON, wantJSON)
			}

			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("hook executed command expansion: %v", err)
			}
		})
	}
}

func TestGuardHookAuxiliaryCaptureBounds(t *testing.T) {
	workspace := t.TempDir()
	paths := []string{filepath.Join(workspace, "first"), filepath.Join(workspace, "second")}
	for _, path := range paths {
		if err := os.WriteFile(path, []byte(strings.Repeat("x", 5<<20)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, exhausted := range []bool{false, true} {
		t.Run(strconv.FormatBool(exhausted), func(t *testing.T) {
			server, err := transport.Listen(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			seen := make(chan *changecapture.Observation, 1)
			command := "*** Begin Patch\n*** Update File: first\n@@\n-x\n+y\n*** End Patch\n"
			if err := server.RegisterBoundary(func(ctx context.Context, request transport.Boundary) {
				if request.Input != command || request.Item != "item" || request.Thread != "thread" || request.Turn != "turn" || request.CWD != workspace || request.Tool != "apply_patch" {
					t.Errorf("native boundary identity/input changed: %+v", request)
				}
				if exhausted {
					<-ctx.Done()
				}
				seen <- changecapture.NamedUntil(paths, workspace, request.Deadline)
			}); err != nil {
				t.Fatal(err)
			}
			request := shellhook.Input{Event: "PreToolUse", Tool: "apply_patch", Thread: "thread", Turn: "turn", Item: "item", CWD: workspace}
			request.Input.Command = &command
			input, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			var output, stderr bytes.Buffer
			started := time.Now()
			code := guardHook("/trusted/helper", []string{"", "", "", filepath.Dir(server.Endpoints().Requests)}, bytes.NewReader(input), &output, &stderr)
			elapsed := time.Since(started)
			if code != 0 || output.String() != "{}\n" || stderr.Len() != 0 {
				t.Fatalf("auxiliary capture changed approval: code=%d output=%s stderr=%s", code, &output, &stderr)
			}
			if elapsed > transport.BoundaryHold+100*time.Millisecond {
				t.Fatalf("capture hold exceeded deadline: %v", elapsed)
			}
			var observed *changecapture.Observation
			select {
			case observed = <-seen:
			case <-time.After(time.Second):
				t.Fatal("native boundary was not delivered")
			}
			bytes := 0
			omitted := len(observed.Omitted)
			for _, file := range observed.Files {
				bytes += len(file.Content)
				if file.Error != "" {
					omitted++
				}
			}
			if bytes > changecapture.MaxContentBytes || omitted == 0 {
				t.Fatalf("aggregate capture bound: bytes=%d omitted=%+v", bytes, observed.Omitted)
			}
			if exhausted && bytes != 0 {
				t.Fatal("late bytes became before-state")
			}
			t.Logf("helper hold=%v retained text=%d omitted paths=%d", elapsed, bytes, omitted)
		})
	}
}

type hookBrokenIO struct{}

func (hookBrokenIO) Read([]byte) (int, error)  { return 0, errors.New("input unavailable") }
func (hookBrokenIO) Write([]byte) (int, error) { return 0, errors.New("output unavailable") }

func TestGuardHookBlocksFailures(t *testing.T) {
	const valid = `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"echo safe"}}`
	for _, tt := range []struct {
		name   string
		args   []string
		input  io.Reader
		output io.Writer
		reason string
	}{
		{"arguments", nil, strings.NewReader(valid), io.Discard, "expected guard"},
		{"malformed", []string{"", "", ""}, strings.NewReader("{"), io.Discard, "instrumentation failed"},
		{"oversize", []string{"", "", ""}, strings.NewReader(strings.Repeat(" ", (4<<20)+1)), io.Discard, "exceeds 4 MiB"},
		{"read", []string{"", "", ""}, hookBrokenIO{}, io.Discard, "input unavailable"},
		{"event", []string{"", "", ""}, strings.NewReader(`{"hook_event_name":"PostToolUse"}`), io.Discard, "unexpected hook input"},
		{"write", []string{"", "", ""}, strings.NewReader(valid), hookBrokenIO{}, "output unavailable"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			if code := guardHook("/trusted/helper", tt.args, tt.input, tt.output, &stderr); code != 2 || !strings.Contains(stderr.String(), tt.reason) {
				t.Fatalf("code=%d stderr=%s", code, &stderr)
			}
		})
	}
}
