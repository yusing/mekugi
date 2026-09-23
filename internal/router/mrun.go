package router

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yusing/mekugi/internal/router/toolplugin"
)

const (
	mrunDeliveryTokens   = 10_000
	mrunProcessWaitDelay = 2 * time.Second
)

type mrunOptions struct {
	maxLines  int
	maxTokens int
	tail      bool
}

func parseMRunArguments(arguments []string) (mrunOptions, []string, error) {
	var options mrunOptions
	for len(arguments) > 0 {
		switch arguments[0] {
		case "--max-tokens":
			if len(arguments) < 2 || options.maxTokens != 0 {
				return options, nil, fmt.Errorf("--max-tokens requires one integer from 1 to %d and cannot repeat", maxOutputTokens)
			}
			value := arguments[1]
			number, err := strconv.Atoi(value)
			if err != nil || number < 1 || number > maxOutputTokens || strconv.Itoa(number) != value {
				return options, nil, fmt.Errorf("--max-tokens requires one integer from 1 to %d", maxOutputTokens)
			}
			options.maxTokens = number
			arguments = arguments[2:]
		case "-n":
			if len(arguments) < 2 || options.maxLines != 0 {
				return options, nil, fmt.Errorf("-n requires one positive integer and cannot repeat")
			}
			number, err := strconv.Atoi(arguments[1])
			if err != nil || number < 1 || strconv.Itoa(number) != arguments[1] {
				return options, nil, fmt.Errorf("-n requires one positive integer")
			}
			options.maxLines = number
			arguments = arguments[2:]

		case "--tail":
			if options.tail {
				return options, nil, fmt.Errorf("--tail cannot repeat")
			}
			options.tail = true
			arguments = arguments[1:]
		case "--":
			if (options.maxTokens == 0 && options.maxLines == 0) || len(arguments) < 2 || arguments[1] == "" {
				return options, nil, fmt.Errorf("expected -n N or --max-tokens N, optionally --tail, then -- COMMAND [ARG...]")
			}
			return options, arguments[1:], nil
		default:
			return options, nil, fmt.Errorf("expected -n N or --max-tokens N, optionally --tail, then -- COMMAND [ARG...]")
		}
	}
	return options, nil, fmt.Errorf("expected -n N or --max-tokens N, optionally --tail, then -- COMMAND [ARG...]")
}

// mrunCapture drains every write. Token-only tail mode uses a byte ring;
// line mode retains complete lines, bounding each candidate when tokens are limited.
type mrunCapture struct {
	maxLines     int
	rows         []string
	rowStart     int
	pendingBytes *mrunCapture
	pending      strings.Builder
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

// Line selection retains complete LF-delimited lines, including an unterminated
// final line. With a token ceiling, only the admissible bytes of each line are retained.
func (capture *mrunCapture) appendLine(line string) {
	if len(capture.rows) < capture.maxLines {
		capture.rows = append(capture.rows, line)
		return
	}
	capture.omitted = true
	if capture.tail {
		capture.rows[capture.rowStart] = line
		capture.rowStart = (capture.rowStart + 1) % capture.maxLines
	}
}

func (capture *mrunCapture) writeLinePart(value []byte) {
	if len(capture.buffer) <= utf8.UTFMax {
		capture.pending.Write(value)
		return
	}
	if capture.pendingBytes == nil {
		// Reuse the line capture's otherwise idle byte buffer. Scan all
		// input for newlines even after this candidate window is full.
		capture.pendingBytes = &mrunCapture{buffer: capture.buffer, tail: capture.tail}
	}
	capture.pendingBytes.Write(value)
}

func (capture *mrunCapture) finishLine() {
	if capture.pendingBytes == nil {
		capture.appendLine(capture.pending.String())
		capture.pending.Reset()
		return
	}
	capture.appendLine(capture.pendingBytes.text())
	capture.omitted = capture.omitted || capture.pendingBytes.omitted
	*capture.pendingBytes = mrunCapture{buffer: capture.buffer, tail: capture.tail}
}

func (capture *mrunCapture) writeLines(value []byte) (int, error) {
	length := len(value)
	for len(value) > 0 {
		if !capture.tail && len(capture.rows) == capture.maxLines {
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
		if capture.pending.Len() > 0 || (capture.pendingBytes != nil && capture.pendingBytes.size > 0) {
			capture.finishLine()
		}
		// Apply the token byte reserve before joining retained rows, so the
		// tokenizer window does not allocate the entire line selection.
		if len(capture.buffer) > utf8.UTFMax {
			window := mrunCapture{buffer: capture.buffer, tail: capture.tail}
			spans := [][]string{capture.rows[capture.rowStart:], capture.rows[:capture.rowStart]}
			total := 0
			for _, rows := range spans {
				for _, row := range rows {
					total += len(row)
				}
			}
			window.omitted = total > len(window.buffer)
			skip := 0
			if capture.tail {
				skip = max(0, total-len(window.buffer))
			}
			for _, rows := range spans {
				for _, row := range rows {
					if skip >= len(row) {
						skip -= len(row)
						continue
					}
					row = row[skip:]
					skip = 0
					// Copy directly into the existing byte capture rather than
					// allocating a byte conversion for each retained row.
					window.size += copy(window.buffer[window.size:], row)
					if window.size == len(window.buffer) {
						break
					}
				}
				if window.size == len(window.buffer) {
					break
				}
			}
			value := window.text()
			capture.omitted = capture.omitted || window.omitted
			return value
		}
		value := strings.Join(capture.rows[capture.rowStart:], "") + strings.Join(capture.rows[:capture.rowStart], "")
		return strings.ToValidUTF8(value, "\uFFFD")
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

func executeMRun(
	ctx context.Context,
	manifest toolWorkerManifest,
	runtimeRoot string,
	arguments []string,
	stdin *os.File,
) (toolplugin.ExecutionOutput, error) {
	options, command, err := parseMRunArguments(arguments)
	if err != nil {
		return toolplugin.ExecutionOutput{Stderr: fmt.Sprintf("mrun: %v\n", err), ExitCode: 2}, nil
	}
	// Source: plugins/tokens.ts MAX_POSSIBLE_GPT5_TOKEN_BYTES.
	// No GPT-5 token spans more than 128 bytes. Keep a small UTF-8 boundary
	// reserve, separately for each stream, before exact final token selection.
	byteLimit := options.maxTokens*128 + utf8.UTFMax
	stdout := mrunCapture{buffer: make([]byte, byteLimit), tail: options.tail, maxLines: options.maxLines}
	stderr := mrunCapture{buffer: make([]byte, byteLimit), tail: options.tail, maxLines: options.maxLines}
	path, err := exec.LookPath(command[0])
	if err != nil {
		return toolplugin.ExecutionOutput{Stderr: fmt.Sprintln(err), ExitCode: 127}, nil
	}
	child := exec.CommandContext(ctx, path, command[1:]...)
	child.Args = command
	child.Stdin, child.Stdout, child.Stderr = stdin, &stdout, &stderr
	child.WaitDelay = mrunProcessWaitDelay
	runErr := child.Run()
	if ctx.Err() != nil {
		return toolplugin.ExecutionOutput{}, ctx.Err()
	}
	exitCode := 0
	if runErr != nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](runErr); ok {
			exitCode = mrunProcessExitCode(exitErr)
		} else if _, ok := errors.AsType[*exec.Error](runErr); ok {
			return toolplugin.ExecutionOutput{Stderr: fmt.Sprintln(runErr), ExitCode: 127}, nil
		} else {
			return toolplugin.ExecutionOutput{}, runErr
		}
	}

	// Preserve streams and prioritize diagnostics when they compete for the
	// shared command-output budget. The fixed omission notice is outside it.
	errText, outText := stderr.text(), stdout.text()
	mode := "head"
	if options.tail {
		mode = "tail"
	}
	selectedOut, selectedErr := outText, errText
	if options.maxTokens > 0 && len(outText)+len(errText) > options.maxTokens {
		// A token always contains at least one source byte. Only invoke the exact
		// tokenizer when the captured byte count cannot prove the output fits.
		formatted, err := toolplugin.FormatOutput(ctx, manifest.NodeExecutable, runtimeRoot,
			[]string{strconv.Itoa(options.maxTokens), mode, outText, errText})
		if err != nil {
			return toolplugin.ExecutionOutput{}, fmt.Errorf("select output: %w", err)
		}
		if formatted.ExitCode != 0 {
			return toolplugin.ExecutionOutput{}, errors.New("output selection failed")
		}
		selectedOut, selectedErr = formatted.Stdout, formatted.Stderr
	}
	if stdout.omitted || stderr.omitted || selectedOut != outText || selectedErr != errText {
		separator := ""
		if selectedErr != "" && !strings.HasSuffix(selectedErr, "\n") {
			separator = "\n"
		}
		limit := fmt.Sprintf("%d-token limit", options.maxTokens)
		if options.maxLines > 0 {
			limit = fmt.Sprintf("%d-line limit", options.maxLines)
			if options.maxTokens > 0 {
				limit += fmt.Sprintf(" or %d-token limit", options.maxTokens)
			}
		}
		selectedErr += fmt.Sprintf("%smrun: output incomplete: %s reached\n", separator, limit)
	}
	execution := toolplugin.ExecutionOutput{Stdout: selectedOut, Stderr: selectedErr, ExitCode: exitCode}
	if len(selectedOut)+len(selectedErr) <= mrunDeliveryTokens {
		return execution, nil
	}
	formatted, err := toolplugin.FormatOutput(ctx, manifest.NodeExecutable, runtimeRoot,
		[]string{strconv.Itoa(mrunDeliveryTokens), "head", selectedOut, selectedErr})
	if err != nil {
		return toolplugin.ExecutionOutput{}, fmt.Errorf("select retained output: %w", err)
	}
	if formatted.ExitCode != 0 || !strings.HasPrefix(selectedOut, formatted.Stdout) || !strings.HasPrefix(selectedErr, formatted.Stderr) {
		return toolplugin.ExecutionOutput{}, errors.New("retained output selection failed")
	}
	execution.Stdout, execution.Stderr = formatted.Stdout, formatted.Stderr
	omitted := toolplugin.OmittedOutput{
		Stdout: selectedOut[len(formatted.Stdout):],
		Stderr: selectedErr[len(formatted.Stderr):],
	}
	if omitted.Stdout != "" || omitted.Stderr != "" {
		execution.OmittedOutput = &omitted
		if execution.Stderr != "" && !strings.HasSuffix(execution.Stderr, "\n") {
			execution.Stderr += "\n"
		}
	}
	return execution, nil
}
