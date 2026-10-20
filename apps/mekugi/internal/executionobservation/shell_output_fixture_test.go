package executionobservation

import (
	"context"

	"github.com/yusing/mekugi/contracts/command"
)

func (s *mekugiReplayStore) putShellOutput(ctx context.Context, stdout, stderr string, exitCode int) (string, error) {
	return s.publishOutput(ctx, command.OmittedOutput{Stdout: stdout, Stderr: stderr}, exitCode)
}
