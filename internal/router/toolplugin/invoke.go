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
)

func invoke(
	ctx context.Context,
	node, hostPath, isolatedCWD, processDirectory string,
	processEnvironment []string,
	outputLimit int64,
	inheritedInput *os.File,
	transientExtraFiles []*os.File,
	request, response any,
) error {
	encoded, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("encode plugin runtime request: %w", err)
	}
	command := exec.CommandContext(ctx, node, hostPath)
	ConfigureProcessGroup(command)
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
	stdoutPipe, err := command.StdoutPipe()
	if err != nil {
		return fmt.Errorf("capture plugin runtime output: %w", err)
	}
	stderrPipe, err := command.StderrPipe()
	if err != nil {
		return fmt.Errorf("capture plugin runtime diagnostics: %w", err)
	}
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

	// Drain before Wait so os/exec's context watcher remains responsible for
	// descendants retaining these pipes after the host exits.
	type capturedOutput struct {
		data []byte
		err  error
	}
	capture := func(reader io.Reader) <-chan capturedOutput {
		done := make(chan capturedOutput, 1)
		go func() {
			data, err := io.ReadAll(io.LimitReader(reader, outputLimit+1))
			if err == nil {
				_, err = io.Copy(io.Discard, reader)
			}
			done <- capturedOutput{data, err}
		}()
		return done
	}
	stdoutResult := capture(stdoutPipe)
	stderrResult := capture(stderrPipe)
	stdout, stderr := <-stdoutResult, <-stderrResult
	runErr := command.Wait()

	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	if err := errors.Join(stdout.err, stderr.err); err != nil {
		return fmt.Errorf("read plugin runtime output: %w", err)
	}
	if int64(len(stdout.data)) > outputLimit || int64(len(stderr.data)) > outputLimit {
		return fmt.Errorf("plugin runtime output exceeds %d bytes", outputLimit)
	}
	if runErr != nil {
		diagnostic := strings.TrimSpace(string(stderr.data))
		if diagnostic == "" {
			return fmt.Errorf("invoke plugin runtime: %w", runErr)
		}
		return fmt.Errorf("invoke plugin runtime: %w: %s", runErr, diagnostic)
	}
	decoder := json.NewDecoder(bytes.NewReader(stdout.data))
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
		// The host has released its interpreter pipes and returned the bounded
		// result. Retire only this invocation's explicitly requested remaining group.
		if err := command.Cancel(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return fmt.Errorf("retire plugin process group: %w", err)
		}
	}
	return nil
}
