package router

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/yusing/mekugi/capturer"
	"github.com/yusing/mekugi/internal/router/toolplugin"
	"github.com/yusing/mekugi/internal/shellruntime"
	"github.com/yusing/mekugi/internal/shellsyntax"
	"golang.org/x/term"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

var shellOverflowDiagnostic = fmt.Sprintf(
	"shell: interpreter output exceeds %d bytes\n",
	toolplugin.ExecutionOutputBudgetBytes,
)

func executeShellTool(
	ctx context.Context,
	manifest toolWorkerManifest,
	runtimeRoot string,
	shellContribution *toolContribution,
	arguments []string,
	stdin *os.File,
	workingDirectory string,
	environment []string,
	commentary shellCommentarySink,
	streamStdout, streamStderr io.Writer,
) (toolplugin.ExecutionOutput, error) {
	if len(arguments) < 2 {
		return executeShellProgram(ctx, manifest, runtimeRoot, shellContribution, arguments, stdin,
			workingDirectory, environment, commentary, streamStdout, streamStderr)
	}
	invocation, source, err := parseShellInvocation(arguments[len(arguments)-1])
	if err != nil {
		return toolplugin.ExecutionOutput{}, err
	}
	arguments = slices.Clone(arguments)
	arguments[len(arguments)-1] = source
	ctx = context.WithValue(ctx, shellInvocationContextKey{}, invocation)
	commentary = invocation.commentary(commentary)
	parsed, err := shellsyntax.Parse(arguments[len(arguments)-1])
	if err != nil {
		return toolplugin.ExecutionOutput{}, err
	}
	programArguments := arguments
	interpreter := shellInterpreterName(arguments[0])
	if interpreter == "bash" || interpreter == "sh" {
		programArguments = slices.Clone(arguments)
		programArguments[len(programArguments)-1] = parsed.Body
	}
	tokens := 10000
	if value, ok := parsed.Params["max_output_tokens"].(float64); ok && value >= 1 && value <= 1<<30 && value == float64(int(value)) {
		tokens = int(value)
	}
	ctx, closeFormatter := toolplugin.WithOutputFormatter(ctx, manifest.NodeExecutable, runtimeRoot)
	defer closeFormatter()
	display := newShellOutputDisplay(ctx, manifest, runtimeRoot, tokens, streamStdout, streamStderr)
	execution, err := executeShellProgram(ctx, manifest, runtimeRoot, shellContribution, programArguments, stdin,
		workingDirectory, environment, commentary, &display.streams[0], &display.streams[1])
	if err != nil {
		return execution, err
	}
	return display.finish(execution)
}

func executeShellProgram(
	ctx context.Context,
	manifest toolWorkerManifest,
	runtimeRoot string,
	shellContribution *toolContribution,
	arguments []string,
	stdin *os.File,
	workingDirectory string,
	environment []string,
	commentary shellCommentarySink,
	streamStdout, streamStderr io.Writer,
) (toolplugin.ExecutionOutput, error) {
	if commentary != nil {
		defer func() {
			completionContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			defer cancel()
			_ = commentary.Complete(completionContext)
		}()
	}
	if len(arguments) < 2 {
		return toolplugin.ExecutionOutput{Stderr: "shell: missing interpreter or script body\n", ExitCode: 1}, nil
	}
	interpreter := shellInterpreterName(arguments[0])
	if interpreter != "bash" && interpreter != "sh" {
		return toolplugin.Execute(
			ctx,
			manifest.NodeExecutable,
			runtimeRoot,
			shellContribution.Module,
			shellContribution.ModuleIndex,
			arguments,
			stdin,
			workingDirectory,
			environment,
		)
	}

	variant := syntax.LangBash
	if interpreter == "sh" {
		variant = syntax.LangPOSIX
	}
	program, err := syntax.NewParser(syntax.Variant(variant)).Parse(
		strings.NewReader(arguments[len(arguments)-1]),
		"",
	)
	if err != nil {
		return toolplugin.ExecutionOutput{Stderr: fmt.Sprintf("shell: %v\n", err), ExitCode: 2}, nil
	}

	syntax.Walk(program, func(node syntax.Node) bool {
		// mvdan does not implement >|. It is an unconditional truncating
		// redirection, which the worker owns just like >.
		if redirect, ok := node.(*syntax.Redirect); ok && redirect.Op == syntax.RdrClob {
			redirect.Op = syntax.RdrOut
		}
		return true
	})

	shellID := rand.Text()
	builtinDispatch := "__mekugi_builtin_" + shellID
	if err := rewriteShellTime(program, builtinDispatch); err != nil {
		return toolplugin.ExecutionOutput{Stderr: err.Error() + "\n", ExitCode: 2}, nil
	}
	privateTools := make(map[string]toolContribution)
	for _, contribution := range manifest.Tools {
		if contribution.PluginID == builtinToolsPluginID && !contribution.ModelVisible && contribution.Name != "mcat" {
			privateTools[contribution.Name] = contribution
		}
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	capture := newShellOutputCapture(cancel)
	capture.stdout.destination = streamStdout
	capture.stderr.destination = streamStderr
	files := &shellFileTracker{manifest: manifest, sink: commentary, notices: &capture.stderr}
	terminalShell := stdin != nil && term.IsTerminal(int(stdin.Fd()))
	middleware := func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
		return func(handlerCtx context.Context, command []string) (runErr error) {
			// An early-closing pipeline consumer is a command failure, not an
			// interpreter failure. Leave pipefail and errexit to the shell.
			defer func() {
				if errors.Is(runErr, syscall.EPIPE) || errors.Is(runErr, io.ErrClosedPipe) {
					runErr = interp.ExitStatus(141)
				}
			}()

			if command[0] == builtinDispatch {
				return executeShellBuiltin(handlerCtx, command[1:], privateTools, terminalShell, builtinDispatch)
			}
			if command[0] == "hpatch" {
				return executeHpatch(handlerCtx, manifest, command[1:], commentary)
			}
			if command[0] == "hchanges" {
				return executeHChanges(handlerCtx, manifest, runtimeRoot, command[1:])
			}
			if command[0] == "hrun" {
				return executeHRun(handlerCtx, manifest, runtimeRoot, command[1:], terminalShell)
			}
			contribution, private := privateTools[command[0]]
			if !private {
				return files.command(handlerCtx, command, next)
			}
			handler := interp.HandlerCtx(handlerCtx)
			journal := manifest.AXReadOutput
			if journal == "" {
				journal = handler.Env.Get(capturer.AXReadOutputEnvironment).String()
			}
			invocation, _ := handlerCtx.Value(shellInvocationContextKey{}).(shellInvocation)
			callID := invocation.CallID
			observation, observationErr := capturer.StartAXReadWithContext(journal,
				handler.Env.Get(shellruntime.ThreadIDEnvironment).String(), contribution.Name,
				capturer.AXReadContext{CallID: callID, ShellID: shellID})
			failureClass := ""
			var exitCode *int
			if observationErr != nil {
				_, _ = fmt.Fprintf(handler.Stderr, "shell: AX read evidence unavailable: %v\n", observationErr)
			}
			defer func() {
				if errors.Is(handlerCtx.Err(), context.DeadlineExceeded) && runErr != nil {
					failureClass = "deadline_exceeded"
				} else if handlerCtx.Err() != nil && runErr != nil {
					failureClass = "canceled"
				}
				if err := observation.FinishResult(runErr == nil, failureClass, exitCode); err != nil {
					_, _ = fmt.Fprintf(handler.Stderr, "shell: AX read evidence incomplete: %v\n", err)
				}
			}()
			arguments := command[1:]
			input, _ := handler.Stdin.(*os.File)
			execution, executeErr := toolplugin.Execute(
				handlerCtx,
				manifest.NodeExecutable,
				runtimeRoot,
				contribution.Module,
				contribution.ModuleIndex,
				arguments,
				input,
				handler.Dir,
				shellEnvironment(handler.Env),
			)
			if executeErr == nil {
				execution, executeErr = retainExecutionOutput(handlerCtx, manifest, execution)
			}
			if executeErr != nil {
				failureClass = "execution_error"
				_, _ = fmt.Fprintf(handler.Stderr, "%s: %v\n", command[0], executeErr)
				return interp.ExitStatus(1)
			}
			exitCode = new(execution.ExitCode)
			if _, writeErr := io.WriteString(handler.Stdout, execution.Stdout); writeErr != nil {
				failureClass = "output_write"
				return fmt.Errorf("write %s stdout: %w", command[0], writeErr)
			}
			if _, writeErr := io.WriteString(handler.Stderr, execution.Stderr); writeErr != nil {
				failureClass = "output_write"
				return fmt.Errorf("write %s stderr: %w", command[0], writeErr)
			}
			if execution.ExitCode != 0 {
				failureClass = execution.FailureClass
				return interp.ExitStatus(execution.ExitCode)
			}
			return nil
		}
	}

	runner, err := interp.New(
		interp.Env(expand.ListEnviron(environment...)),
		interp.Dir(workingDirectory),
		interp.Params(arguments[1:len(arguments)-1]...),
		interp.StdIO(stdin, &capture.stdout, &capture.stderr),
		interp.ExecHandler(middleware(func(ctx context.Context, arguments []string) error {
			handler := interp.HandlerCtx(ctx)
			// Pipes, redirections and substitutions consume program data, not
			// display output. Private readers were already dispatched above.
			if !terminalShell && handler.Stdout == &capture.stdout && handler.Stderr == &capture.stderr && (handler.Stdin == stdin || handler.Stdin == nil && stdin == nil) {
				arguments = routeShellCommand(ctx, manifest, runtimeRoot, arguments, handler)
			}
			return runExternalShellCommand(ctx, arguments, terminalShell, handler)
		})),
		interp.OpenHandler(files.openWrite),
		interp.CallHandler(shellCommentaryCallHandler(commentary)),
	)
	if err != nil {
		return toolplugin.ExecutionOutput{Stderr: fmt.Sprintf("shell: %v\n", err), ExitCode: 1}, nil
	}
	// Initial functions supply missing builtins while allowing authored functions
	// to replace them through the interpreter's ordinary function lookup.
	var builtinPrelude strings.Builder
	for _, name := range []string{"type", "kill", "printf", "read", "ulimit"} {
		fmt.Fprintf(&builtinPrelude, "%s() { %s %s \"$@\"; };\n", name, builtinDispatch, name)
	}
	prelude, err := syntax.NewParser(syntax.Variant(variant)).Parse(strings.NewReader(builtinPrelude.String()), "")
	if err != nil {
		return toolplugin.ExecutionOutput{}, err
	}
	if err := runner.Run(runCtx, prelude); err != nil {
		return toolplugin.ExecutionOutput{}, err
	}
	runErr := runner.Run(runCtx, program)
	if evidenceErr := files.finish(); evidenceErr != nil {
		fmt.Fprintln(&capture.stderr, evidenceErr)
		if runErr == nil {
			runErr = interp.ExitStatus(1)
		}
	}
	stdout, stderr, overflow, writeErr := capture.result()
	if ctx.Err() != nil {
		return toolplugin.ExecutionOutput{}, ctx.Err()
	}
	if writeErr != nil {
		return toolplugin.ExecutionOutput{}, writeErr
	}
	if overflow {
		var stdoutValid, stderrValid bool
		stdout, stdoutValid = trimIncompleteUTF8Tail(stdout)
		stderr, stderrValid = trimIncompleteUTF8Tail(stderr)
		if !stdoutValid || !stderrValid {
			return toolplugin.ExecutionOutput{Stderr: "shell: interpreter output is not UTF-8\n", ExitCode: 1}, nil
		}
		return toolplugin.ExecutionOutput{Stdout: stdout, Stderr: stderr + shellOverflowDiagnostic, ExitCode: 1}, nil
	}
	if !utf8.ValidString(stdout) || !utf8.ValidString(stderr) {
		return toolplugin.ExecutionOutput{Stderr: "shell: interpreter output is not UTF-8\n", ExitCode: 1}, nil
	}
	if runErr == nil {
		return toolplugin.ExecutionOutput{Stdout: stdout, Stderr: stderr, ExitCode: 0}, nil
	}
	if status, ok := errors.AsType[interp.ExitStatus](runErr); ok {
		return toolplugin.ExecutionOutput{Stdout: stdout, Stderr: stderr, ExitCode: int(status)}, nil
	}
	return toolplugin.ExecutionOutput{
		Stdout:   stdout,
		Stderr:   stderr + fmt.Sprintf("shell: %v\n", runErr),
		ExitCode: 1,
	}, nil
}

// trimIncompleteUTF8Tail removes trailing incomplete UTF-8 sequences from value.
func trimIncompleteUTF8Tail(value string) (string, bool) {
	if utf8.ValidString(value) {
		return value, true
	}
	encoded := []byte(value)
	start := len(encoded) - 1
	for start > 0 && !utf8.RuneStart(encoded[start]) {
		start--
	}
	if utf8.FullRune(encoded[start:]) || !utf8.Valid(encoded[:start]) {
		return "", false
	}
	return string(encoded[:start]), true
}

func runExternalShellCommand(ctx context.Context, arguments []string, terminalShell bool, handler interp.HandlerContext) error {
	path, err := interp.LookPathDir(handler.Dir, handler.Env, arguments[0])
	if err != nil {
		_, _ = fmt.Fprintln(handler.Stderr, err)
		return interp.ExitStatus(127)
	}
	command := exec.CommandContext(ctx, path, arguments[1:]...)
	command.Args = arguments
	command.Dir = handler.Dir
	command.Env = shellEnvironment(handler.Env)
	command.Stdin = handler.Stdin
	command.Stdout = handler.Stdout
	command.Stderr = handler.Stderr
	// Keep real file descriptors for external commands (including cat self-copy
	// detection); the owning wrapper captures through its descriptor on close.
	if file, ok := handler.Stdout.(*shellFileWrite); ok {
		command.Stdout = file.file
	}
	if file, ok := handler.Stderr.(*shellFileWrite); ok {
		command.Stderr = file.file
	}
	if terminalShell {
		// mvdan does not coordinate terminal foreground-group handoff across a
		// pipeline, so every command in a PTY-backed shell must remain in the
		// worker's foreground group. WaitDelay still bounds inherited pipes.
		command.WaitDelay = 2 * time.Second
	} else {
		toolplugin.ConfigureProcessGroup(command)
	}
	err = command.Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		return nil
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		return interp.ExitStatus(shellProcessExitCode(exitErr))
	}
	if execErr, ok := errors.AsType[*exec.Error](err); ok {
		_, _ = fmt.Fprintln(handler.Stderr, execErr)
		return interp.ExitStatus(127)
	}
	return err
}

// shellInterpreterName normalizes an interpreter path for policy comparisons.
func shellInterpreterName(interpreter string) string {
	return shellsyntax.InterpreterIdentity(interpreter)
}

// shellEnvironment converts mvdan/sh environment variables to sorted KEY=VALUE pairs.
func shellEnvironment(environment expand.Environ) []string {
	values := make(map[string]string)
	environment.Each(func(name string, variable expand.Variable) bool {
		if variable.IsSet() && variable.Exported {
			values[name] = variable.String()
		}
		return true
	})
	names := slices.Sorted(maps.Keys(values))
	result := make([]string, 0, len(names))
	for _, name := range names {
		result = append(result, name+"="+values[name])
	}
	return result
}

// shellOutputCapture bounds mvdan/sh interpreter output to the execution budget.
type shellOutputCapture struct {
	mu        sync.Mutex
	stdout    shellOutputWriter
	stderr    shellOutputWriter
	remaining int
	overflow  bool
	cancel    context.CancelFunc
	writeErr  error
	closed    bool
}

// shellOutputWriter is one output stream for the shell output capture.
type shellOutputWriter struct {
	capture     *shellOutputCapture
	buffer      bytes.Buffer
	destination io.Writer
	forwarded   int
	invalid     bool
}

// newShellOutputCapture creates a bounded output capture for shell execution.
func newShellOutputCapture(cancel context.CancelFunc) *shellOutputCapture {
	// Match the JavaScript executor's three-byte UTF-8 boundary reserve so both
	// interpreter paths keep the overflow diagnostic inside the shared budget.
	capture := &shellOutputCapture{
		remaining: max(0, toolplugin.ExecutionOutputBudgetBytes-len(shellOverflowDiagnostic)-3),
		cancel:    cancel,
	}
	capture.stdout.capture = capture
	capture.stderr.capture = capture
	return capture
}

// Write captures output bytes up to the remaining budget and cancels on overflow.
func (writer *shellOutputWriter) Write(value []byte) (int, error) {
	capture := writer.capture
	capture.mu.Lock()
	defer capture.mu.Unlock()
	var forward func([]byte) (int, error)
	if writer.destination != nil {
		forward = writer.destination.Write
	}
	return writer.writeLocked(value, forward)
}

func (writer *shellOutputWriter) writeLocked(value []byte, forward func([]byte) (int, error)) (int, error) {
	written := len(value)
	capture := writer.capture
	if capture.closed || capture.overflow {
		return written, nil
	}
	accepted := min(len(value), capture.remaining)
	_, _ = writer.buffer.Write(value[:accepted])
	capture.remaining -= accepted
	if writer.destination != nil && !writer.invalid && capture.writeErr == nil {
		pending := writer.buffer.Bytes()[writer.forwarded:]
		end := 0
		for end < len(pending) && utf8.FullRune(pending[end:]) {
			r, size := utf8.DecodeRune(pending[end:])
			if r == utf8.RuneError && size == 1 {
				writer.invalid = true
				break
			}
			end += size
		}
		if end > 0 {
			n, err := forward(pending[:end])
			writer.forwarded += n
			if err == nil && n != end {
				err = io.ErrShortWrite
			}
			if err != nil {
				capture.writeErr = fmt.Errorf("write shell output: %w", err)
				capture.cancel()
				return written, capture.writeErr
			}
		}
	}
	if accepted != len(value) {
		capture.overflow = true
		capture.cancel()
	}
	return written, nil
}

// result closes forwarding and snapshots unforwarded output and failures.
func (capture *shellOutputCapture) result() (stdout, stderr string, overflow bool, writeErr error) {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	capture.closed = true
	return capture.stdout.buffer.String()[capture.stdout.forwarded:], capture.stderr.buffer.String()[capture.stderr.forwarded:], capture.overflow, capture.writeErr
}
