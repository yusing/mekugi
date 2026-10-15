package router

import (
	"bytes"
	"context"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/yusing/mekugi/contracts/command"
)

func TestFrontendRecoveryFailurePreservesAdmittedOutput(t *testing.T) {
	registry := sharedProxyTestRegistry(t)
	// The raw omissions fit, but JSON escaping exceeds the encoded-record bound.
	omitted := strings.Repeat("\x01", 6<<20) + "\n"
	for _, originalStatus := range []int{0, 23} {
		t.Run(strconv.Itoa(originalStatus), func(t *testing.T) {
			commands := slices.Clone(testCommands())
			const admitted = "admitted command rows\n"
			const diagnostic = "original diagnostic"
			for i := range commands {
				if commands[i].Name == "mrun" {
					commands[i].Run = func(context.Context, []string, *os.File) (command.Output, error) {
						return command.Output{Stdout: admitted, Stderr: diagnostic, ExitCode: originalStatus, OmittedOutput: &command.OmittedOutput{Stdout: omitted, StdoutKind: "rows"}}, nil
					}
				}
			}
			var stdout, stderr bytes.Buffer
			handled, status := RunToolPluginWorker(testHistory(), commands, t.Context(), registry.frontends["mrun"], nil, nil, &stdout, &stderr)
			wantStatus := originalStatus
			if wantStatus == 0 {
				wantStatus = 1
			}
			if !handled || status != wantStatus || stdout.String() != admitted {
				t.Fatalf("handled=%t status=%d stdout=%q stderr=%q", handled, status, stdout.String(), stderr.String())
			}
			if !strings.HasPrefix(stderr.String(), diagnostic+"\nmrun: recovery unavailable: ") || !strings.Contains(stderr.String(), "encoded read record") || strings.Contains(stderr.String(), "next_call") {
				t.Fatalf("failed publication diagnostics: %q", stderr.String())
			}
		})
	}
}
