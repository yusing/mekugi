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

// trackingHook reads the host event, then binds resources from the trusted command
// arguments. Event metadata cannot replace the tracker or capture channel.
func trackingHook(args []string, input io.Reader, output, stderr io.Writer) int {
	deadline := time.Now().Add(transport.BoundaryHold)
	fail := func(err error) int {
		fmt.Fprintln(stderr, "mekugi: shell instrumentation failed:", err)
		return 2
	}
	if len(args) != 1 && len(args) != 2 {
		return fail(errors.New("expected tracker and optional capture channel"))
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
	result, err := shellhook.Rewrite(request, args[0])
	if err != nil {
		return fail(err)
	}
	if len(args) == 2 && request.Input.Command != nil {
		transport.ObserveBoundary(args[1], transport.Boundary{Thread: request.Thread, Turn: request.Turn, Item: request.Item, Tool: request.Tool, CWD: request.CWD, Input: *request.Input.Command, Deadline: deadline})
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
