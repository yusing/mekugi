package main

import (
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/yusing/mekugi/backends/codex/shellhook"
	"github.com/yusing/mekugi/modules/commandsegments/transport"
)

// guardHook reads the host event, then binds resources from the trusted command
// arguments. Event metadata cannot replace the invocation's helper or channels.
func guardHook(helper string, args []string, input io.Reader, output, stderr io.Writer) int {
	deadline := time.Now().Add(transport.BoundaryHold)
	fail := func(err error) int {
		fmt.Fprintln(stderr, "mekugi: VCS guard instrumentation failed:", err)
		return 2
	}
	if len(args) != 3 && len(args) != 4 {
		return fail(errors.New("expected guard, sudo, and tracking resources"))
	}
	const limit = 4 << 20
	data, err := io.ReadAll(io.LimitReader(input, limit+1))
	if err != nil {
		return fail(err)
	}
	if len(data) > limit {
		return fail(errors.New("hook input exceeds 4 MiB"))
	}
	var request shellhook.Input
	if err := json.Unmarshal(data, &request); err != nil {
		return fail(err)
	}
	request.Helper = helper
	request.Directory, request.SudoDirectory, request.Tracker = args[0], args[1], args[2]
	result, err := shellhook.Rewrite(request)
	if err != nil {
		return fail(err)
	}
	if len(args) == 4 && request.Input.Command != nil {
		transport.ObserveBoundary(args[3], transport.Boundary{Thread: request.Thread, Turn: request.Turn, Item: request.Item, Tool: request.Tool, CWD: request.CWD, Input: *request.Input.Command, Deadline: deadline})
	}
	data, err = json.Marshal(result)
	if err != nil {
		return fail(err)
	}
	if _, err := output.Write(append(data, '\n')); err != nil {
		return fail(err)
	}
	return 0
}
