package router

import (
	"bytes"
	"context"
	"slices"
	"testing"

	"github.com/yusing/mekugi/contracts/command"
)

func TestAuthenticatedCompiledFrontendProcessScope(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	for _, tc := range []struct {
		name  string
		owned bool
		want  command.ProcessScope
	}{
		{"host", false, command.HostProcessGroup},
		{"dedicated", true, command.DedicatedHostProcess},
	} {
		t.Run(tc.name, func(t *testing.T) {
			commands := slices.Clone(testCommands())
			calls := 0
			var scope command.ProcessScope
			for i := range commands {
				if commands[i].Name == "msymbol" {
					// Exercise authenticated compiled dispatch without enabling real
					// process-wide resolver cleanup in the test runner.
					commands[i].Run = func(_ context.Context, request command.Request) (command.Output, error) {
						calls++
						scope = request.ProcessScope
						return command.Output{Stdout: "scope observed\n"}, nil
					}
				}
			}
			var stdout, stderr bytes.Buffer
			run := runToolPluginWorkerInHostProcess
			if tc.owned {
				run = runOwnedToolPluginWorker
			}
			handled, status := run(testHistory(), testRecovery(), commands, t.Context(), registry.frontends["msymbol"], nil, nil, &stdout, &stderr)
			if !handled || status != 0 || calls != 1 || scope != tc.want || stdout.String() != "scope observed\n" || stderr.Len() != 0 {
				t.Fatalf("handled=%t status=%d calls=%d scope=%v want=%v stdout=%q stderr=%q", handled, status, calls, scope, tc.want, stdout.String(), stderr.String())
			}
		})
	}
}
