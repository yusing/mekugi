package toolplugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func invoke(
	ctx context.Context,
	node, hostPath, isolatedCWD, processDirectory string,
	processEnvironment []string,
	outputLimit int64,
	isolatedProcessGroup bool,
	inheritedInput *os.File,
	transientExtraFiles []*os.File,
	request, response any,
) error {
	encoded, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("encode plugin runtime request: %w", err)
	}
	command := exec.CommandContext(ctx, node, hostPath)
	if isolatedProcessGroup {
		ConfigureProcessGroup(command)
	}
	command.WaitDelay = time.Second
	if inheritedInput != nil {
		command.ExtraFiles = append([]*os.File{inheritedInput}, transientExtraFiles...)
	}
	if isolatedCWD != "" {
		command.Dir = isolatedCWD
		command.Env = []string{
			"HOME=" + filepath.Dir(isolatedCWD),
			"NODE_NO_WARNINGS=1",
			"PATH=" + filepath.Dir(node),
		}
	} else {
		command.Dir = processDirectory
		command.Env = processEnvironment
	}
	command.Stdin = bytes.NewReader(encoded)
	stdout := &boundedHostOutput{limit: outputLimit + 1}
	stderr := &boundedHostOutput{limit: outputLimit + 1}
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Start(); err != nil {
		return fmt.Errorf("start plugin runtime: %w", err)
	}
	var closeErr error
	for _, file := range transientExtraFiles {
		closeErr = errors.Join(closeErr, file.Close())
	}
	if closeErr != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return fmt.Errorf("close inherited plugin runtime files: %w", closeErr)
	}

	runErr := command.Wait()

	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	if int64(stdout.Len()) > outputLimit || int64(stderr.Len()) > outputLimit {
		return fmt.Errorf("plugin runtime output exceeds %d bytes", outputLimit)
	}
	if runErr != nil && !errors.Is(runErr, exec.ErrWaitDelay) {
		diagnostic := strings.TrimSpace(stderr.String())
		if diagnostic == "" {
			return fmt.Errorf("invoke plugin runtime: %w", runErr)
		}
		return fmt.Errorf("invoke plugin runtime: %w: %s", runErr, diagnostic)
	}
	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(response); err != nil {
		return fmt.Errorf("decode plugin runtime result: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("plugin runtime returned trailing output")
	}
	if execution, ok := response.(*executionResponse); ok && execution.TerminationReason != "" {
		if execution.TerminationReason != "resolver_cleanup" && (execution.TerminationReason != "output_limit" || execution.ExitCode == 0) {
			return errors.New("invalid plugin runtime termination reason")
		}
		if enabled, _ := ctx.Value(frontendOrphanCleanupKey{}).(bool); enabled {
			cleanupFrontendOrphans()
		}
		if isolatedProcessGroup {
			if err := command.Cancel(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				return fmt.Errorf("retire plugin process group: %w", err)
			}
		}
	} else if runErr != nil {
		return fmt.Errorf("invoke plugin runtime: %w", runErr)
	}
	return nil
}

// os/exec owns pipe draining and WaitDelay; descendants cannot hold invoke
// forever after the direct host exits. Keep only the bounded result bytes.
type boundedHostOutput struct {
	bytes.Buffer
	limit int64
}

func (b *boundedHostOutput) Write(data []byte) (int, error) {
	available := max(0, b.limit-int64(b.Len()))
	if available > 0 {
		_, _ = b.Buffer.Write(data[:min(int64(len(data)), available)])
	}
	return len(data), nil
}
