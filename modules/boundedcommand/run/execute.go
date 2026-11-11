package run

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/libraries/commandargs"
	"github.com/yusing/mekugi/libraries/outputbudget"
)

const (
	mrunDeliveryTokens   = commandargs.MaxOutputTokens
	mrunProcessWaitDelay = 2 * time.Second
)

// mrunCapture drains every write. Token-only tail mode uses a byte ring;
// combined mode bounds both the pending line and the completed-row window.
type mrunCapture struct {
	maxLines     int
	rows         []string
	rowStart     int
	rowBytes     int
	pendingBytes *mrunCapture
	buffer       []byte
	start        int
	size         int
	tail         bool
	omitted      bool
}

func (capture *mrunCapture) Write(value []byte) (int, error) {
	if capture.maxLines > 0 {
		return capture.writeLines(value)
	}

	length := len(value)
	limit := len(capture.buffer)
	if !capture.tail {
		accepted := copy(capture.buffer[capture.size:], value)
		capture.size += accepted
		capture.omitted = capture.omitted || accepted < length
		return length, nil
	}
	if length >= limit {
		capture.omitted = capture.omitted || capture.size > 0 || length > limit
		copy(capture.buffer, value[length-limit:])
		capture.start, capture.size = 0, limit
		return length, nil
	}
	if excess := capture.size + length - limit; excess > 0 {
		capture.omitted = true
		capture.start = (capture.start + excess) % limit
		capture.size -= excess
	}
	end := (capture.start + capture.size) % limit
	copied := copy(capture.buffer[end:], value)
	copy(capture.buffer, value[copied:])
	capture.size += length
	return length, nil
}

// Combined selection retains only the admissible bytes of each row.
func (capture *mrunCapture) appendBoundedLine(line string) {
	limit := len(capture.buffer)
	if !capture.tail {
		accepted := min(len(line), limit-capture.rowBytes)
		capture.omitted = capture.omitted || accepted < len(line)
		capture.rows = append(capture.rows, line[:accepted])
		capture.rowBytes += accepted
		return
	}

	capture.rows = append(capture.rows, line)
	capture.rowBytes += len(line)
	for len(capture.rows)-capture.rowStart > capture.maxLines || capture.rowBytes > limit {
		capture.omitted = true
		oldest := capture.rows[capture.rowStart]
		excess := capture.rowBytes - limit
		if len(capture.rows)-capture.rowStart <= capture.maxLines && excess < len(oldest) {
			// Keep the raw cutoff until the final window is trimmed. Replacing
			// its split UTF-8 sequence here would shift subsequent byte cutoffs.
			capture.rows[capture.rowStart] = oldest[excess:]
			capture.rowBytes -= excess
			break
		}
		capture.rowBytes -= len(oldest)
		capture.rows[capture.rowStart] = ""
		capture.rowStart++
	}
	if capture.rowStart > 0 && capture.rowStart >= len(capture.rows)/2 {
		remaining := copy(capture.rows, capture.rows[capture.rowStart:])
		clear(capture.rows[remaining:])
		capture.rows = capture.rows[:remaining]
		capture.rowStart = 0
	}
}

func (capture *mrunCapture) writeLinePart(value []byte) {
	if capture.pendingBytes == nil {
		// Reuse the line capture's otherwise idle byte buffer. Scan all
		// input for newlines even after this candidate window is full.
		capture.pendingBytes = &mrunCapture{buffer: capture.buffer, tail: capture.tail}
	}
	capture.pendingBytes.Write(value)
}

func (capture *mrunCapture) finishLine() {
	capture.appendBoundedLine(capture.pendingBytes.text())
	capture.omitted = capture.omitted || capture.pendingBytes.omitted
	*capture.pendingBytes = mrunCapture{buffer: capture.buffer, tail: capture.tail}
}

func (capture *mrunCapture) writeLines(value []byte) (int, error) {
	length := len(value)
	for len(value) > 0 {
		if !capture.tail && (len(capture.rows) == capture.maxLines ||
			capture.rowBytes == len(capture.buffer)) {
			capture.omitted = true
			break
		}
		end := bytes.IndexByte(value, '\n')
		if end < 0 {
			capture.writeLinePart(value)
			break
		}
		capture.writeLinePart(value[:end+1])
		capture.finishLine()
		value = value[end+1:]
	}
	return length, nil
}

func (capture *mrunCapture) text() string {
	if capture.maxLines > 0 {
		if capture.pendingBytes != nil && capture.pendingBytes.size > 0 {
			capture.finishLine()
		}
		window := mrunCapture{buffer: capture.buffer, tail: capture.tail, omitted: capture.omitted}
		for _, row := range capture.rows[capture.rowStart:] {
			window.size += copy(window.buffer[window.size:], row)
		}
		return window.text()
	}

	end := min(capture.start+capture.size, len(capture.buffer))
	value := string(capture.buffer[capture.start:end]) + string(capture.buffer[:capture.size-(end-capture.start)])
	if capture.omitted {
		value = trimMRunBoundary(value, capture.tail)
	}
	// Generic commands may emit arbitrary bytes. Make malformed sequences
	// explicit without turning a successful command into a failed command.
	return strings.ToValidUTF8(value, "\uFFFD")
}

func trimMRunBoundary(value string, tail bool) string {
	if tail {
		for len(value) > 0 && !utf8.RuneStart(value[0]) {
			value = value[1:]
		}
		return value
	}
	for len(value) > 0 {
		_, size := utf8.DecodeLastRuneInString(value)
		if size != 1 || value[len(value)-1] < utf8.RuneSelf {
			break
		}
		value = value[:len(value)-1]
	}
	return value
}

// Execute runs one foreground command under the calling host process.
// Source: internal/router/mrun.go:256:386@[e702dffa] executeMRun/mrunRetainedKind.
func (Service) Execute(ctx context.Context, request command.Request) (command.Output, error) {
	arguments, stdin := request.Args, request.Stdin
	options, argv, err := commandargs.ParseRunOptions(arguments)
	if err != nil {
		return command.Output{Stderr: fmt.Sprintf("mrun: %v\n", err), ExitCode: 2}, nil
	}
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return command.Output{Stderr: fmt.Sprintln(err), ExitCode: 127}, nil
	}
	child := exec.CommandContext(ctx, path, argv[1:]...)
	child.Args = argv
	child.Stdin = stdin
	child.WaitDelay = mrunProcessWaitDelay
	if options.Tokens == 0 {
		return executeMRunRows(ctx, child, options.Lines, options.Tail)
	}
	// Source: plugins/tokens.ts MAX_POSSIBLE_GPT5_TOKEN_BYTES.
	// No GPT-5 token spans more than 128 bytes. Keep a small UTF-8 boundary
	// reserve, separately for each stream, before exact final token selection.
	byteLimit := options.Tokens*128 + utf8.UTFMax
	stdout := mrunCapture{buffer: make([]byte, byteLimit), tail: options.Tail, maxLines: options.Lines}
	stderr := mrunCapture{buffer: make([]byte, byteLimit), tail: options.Tail, maxLines: options.Lines}
	child.Stdout, child.Stderr = &stdout, &stderr
	execution, err := runMRunChild(ctx, child)
	if err != nil || execution.Stderr != "" {
		return execution, err
	}

	// Preserve both streams when they compete for the shared budget. Generated
	// notices are never part of command-output selection or retained data.
	errText, outText := stderr.text(), stdout.text()
	mode := "head"
	if options.Tail {
		mode = "tail"
	}
	selectedOut, selectedErr := outText, errText
	if options.Tokens > 0 && (len(outText)+len(errText) > options.Tokens || outText != "" && len(errText) > options.Tokens/2) {
		// A token always contains at least one source byte. Only invoke the exact
		// tokenizer when the captured byte count cannot prove the output fits.
		formatted, err := outputbudget.Select(ctx, options.Tokens, mode, outText, errText)
		if err != nil {
			return command.Output{}, fmt.Errorf("select output: %w", err)
		}
		selectedOut, selectedErr = formatted.Stdout, formatted.Stderr
	}
	notice := ""
	if stdout.omitted || stderr.omitted || selectedOut != outText || selectedErr != errText {
		limit := fmt.Sprintf("%d-token limit", options.Tokens)
		if options.Lines > 0 {
			limit = fmt.Sprintf("%d-line limit", options.Lines)
			if options.Tokens > 0 {
				limit += fmt.Sprintf(" or %d-token limit", options.Tokens)
			}
		}
		notice = fmt.Sprintf("mrun: output incomplete: %s reached\n", limit)
	}
	execution.Stdout, execution.Stderr = selectedOut, selectedErr

	if len(selectedOut)+len(selectedErr) <= mrunDeliveryTokens && (selectedOut == "" || len(selectedErr) <= mrunDeliveryTokens/2) {
		appendMRunNotice(&execution, notice)
		return execution, nil
	}
	formatted, err := outputbudget.Select(ctx, mrunDeliveryTokens, "rows", selectedOut, selectedErr)
	if err != nil {
		return command.Output{}, fmt.Errorf("select retained output: %w", err)
	}
	if !strings.HasPrefix(selectedOut, formatted.Stdout) || !strings.HasPrefix(selectedErr, formatted.Stderr) {
		return command.Output{}, errors.New("retained output selection failed")
	}
	execution.Stdout, execution.Stderr = formatted.Stdout, formatted.Stderr
	omitted := command.OmittedOutput{
		Stdout: selectedOut[len(formatted.Stdout):],
		Stderr: selectedErr[len(formatted.Stderr):],
	}
	omitted.StdoutKind = mrunRetainedKind(omitted.Stdout)
	omitted.StderrKind = mrunRetainedKind(omitted.Stderr)
	if omitted.Stdout != "" || omitted.Stderr != "" {
		execution.OmittedOutput = &omitted
		if execution.Stderr != "" && !strings.HasSuffix(execution.Stderr, "\n") {
			execution.Stderr += "\n"
		}
	}
	appendMRunNotice(&execution, notice)
	return execution, nil
}

// Generic commands can emit rows larger than any mread page. Keep those streams
// byte-pageable rather than publishing an unusable continuation. Reserve room
// for mread's stream frame and handle label when testing a row's admission.
func mrunRetainedKind(text string) string {
	if !strings.HasSuffix(text, "\n") {
		return ""
	}
	const rowBudget = commandargs.MaxOutputTokens - 64
	budget := new(outputbudget.Budget)
	for row := range strings.SplitAfterSeq(text, "\n") {
		if len(row) <= rowBudget {
			continue
		}
		fits, err := budget.Fits(row, rowBudget)
		if err != nil || !fits {
			return ""
		}
	}
	return "rows"
}

func appendMRunNotice(execution *command.Output, notice string) {
	if notice == "" {
		return
	}
	if execution.Stderr != "" && !strings.HasSuffix(execution.Stderr, "\n") {
		execution.Stderr += "\n"
	}
	execution.Stderr += notice
}

func runMRunChild(ctx context.Context, child *exec.Cmd) (command.Output, error) {
	runErr := child.Run()
	if ctx.Err() != nil {
		return command.Output{}, ctx.Err()
	}
	exitCode := 0
	if runErr != nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](runErr); ok {
			exitCode = mrunProcessExitCode(exitErr)
		} else if _, ok := errors.AsType[*exec.Error](runErr); ok {
			return command.Output{Stderr: fmt.Sprintln(runErr), ExitCode: 127}, nil
		} else {
			return command.Output{}, runErr
		}
	}

	return command.Output{ExitCode: exitCode}, nil
}
