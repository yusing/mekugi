package main

import (
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"

	"github.com/yusing/mekugi/internal/vcsguard"
)

// guardHook reads the host event, then binds resources from the trusted command
// arguments. Event metadata cannot replace the invocation's helper or channels.
func guardHook(helper string, args []string, input io.Reader, output, stderr io.Writer) int {
	fail := func(err error) int {
		fmt.Fprintln(stderr, "mekugi: VCS guard instrumentation failed:", err)
		return 2
	}
	if len(args) != 3 {
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
	var request vcsguard.HookInput
	if err := json.Unmarshal(data, &request); err != nil {
		return fail(err)
	}
	request.Helper = helper
	request.Directory, request.SudoDirectory, request.Tracker = args[0], args[1], args[2]
	result, err := vcsguard.RewriteHook(request)
	if err != nil {
		return fail(err)
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
