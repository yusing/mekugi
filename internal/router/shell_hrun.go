package router

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/yusing/mekugi/internal/router/toolplugin"
	"mvdan.cc/sh/v3/interp"
)

const hrunMaxTokens = 15_500

type hrunOptions struct {
	maxLines  int
	maxTokens int
	tail      bool
}

func parseHRunArguments(arguments []string) (hrunOptions, []string, error) {
	var options hrunOptions
	for len(arguments) > 0 {
		switch arguments[0] {
		case "--max-tokens":
			if len(arguments) < 2 || options.maxTokens != 0 {
				return options, nil, fmt.Errorf("--max-tokens requires one integer from 1 to %d and cannot repeat", hrunMaxTokens)
			}
			value := arguments[1]
			number, err := strconv.Atoi(value)
			if err != nil || number < 1 || number > hrunMaxTokens || strconv.Itoa(number) != value {
				return options, nil, fmt.Errorf("--max-tokens requires one integer from 1 to %d", hrunMaxTokens)
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

// hrunCapture drains every write. Token-only tail mode uses a byte ring;
// line mode retains complete lines, bounding each candidate when tokens are limited.
type hrunCapture struct {
	maxLines     int
	rows         []string
	rowStart     int
	pendingBytes *hrunCapture
	pending      strings.Builder
	buffer       []byte
	start        int
	size         int
	tail         bool
	omitted      bool
}

func (capture *hrunCapture) Write(value []byte) (int, error) {
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
func (capture *hrunCapture) appendLine(line string) {
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

func (capture *hrunCapture) writeLinePart(value []byte) {
	if len(capture.buffer) <= utf8.UTFMax {
		capture.pending.Write(value)
		return
	}
	if capture.pendingBytes == nil {
		// Reuse the line capture's otherwise idle byte buffer. Scan all
		// input for newlines even after this candidate window is full.
		capture.pendingBytes = &hrunCapture{buffer: capture.buffer, tail: capture.tail}
	}
	capture.pendingBytes.Write(value)
}

func (capture *hrunCapture) finishLine() {
	if capture.pendingBytes == nil {
		capture.appendLine(capture.pending.String())
		capture.pending.Reset()
		return
	}
	capture.appendLine(capture.pendingBytes.text())
	capture.omitted = capture.omitted || capture.pendingBytes.omitted
	*capture.pendingBytes = hrunCapture{buffer: capture.buffer, tail: capture.tail}
}

func (capture *hrunCapture) writeLines(value []byte) (int, error) {
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

func (capture *hrunCapture) text() string {
	if capture.maxLines > 0 {
		if capture.pending.Len() > 0 || (capture.pendingBytes != nil && capture.pendingBytes.size > 0) {
			capture.finishLine()
		}
		// Apply the token byte reserve before joining retained rows, so the
		// tokenizer window does not allocate the entire line selection.
		if len(capture.buffer) > utf8.UTFMax {
			window := hrunCapture{buffer: capture.buffer, tail: capture.tail}
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
		value = trimHRunBoundary(value, capture.tail)
	}
	// Generic commands may emit arbitrary bytes. Make malformed sequences
	// explicit without turning a successful command into a failed command.
	return strings.ToValidUTF8(value, "\uFFFD")
}

func trimHRunBoundary(value string, tail bool) string {
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

func executeHRun(ctx context.Context, manifest toolWorkerManifest, runtimeRoot string, shellContribution *toolContribution, arguments []string, terminalShell bool) error {
	handler := interp.HandlerCtx(ctx)
	options, command, err := parseHRunArguments(arguments)
	if err != nil {
		if _, writeErr := fmt.Fprintf(handler.Stderr, "hrun: %v\n", err); writeErr != nil {
			return writeErr
		}
		return interp.ExitStatus(2)
	}
	// Source: plugins/tokens.ts MAX_POSSIBLE_GPT5_TOKEN_BYTES.
	// No GPT-5 token spans more than 128 bytes. Keep a small UTF-8 boundary
	// reserve, separately for each stream, before exact final token selection.
	byteLimit := options.maxTokens*128 + utf8.UTFMax
	stdout := hrunCapture{buffer: make([]byte, byteLimit), tail: options.tail, maxLines: options.maxLines}
	stderr := hrunCapture{buffer: make([]byte, byteLimit), tail: options.tail, maxLines: options.maxLines}
	childHandler := handler
	childHandler.Stdout, childHandler.Stderr = &stdout, &stderr
	runErr := runExternalShellCommand(ctx, command, terminalShell, childHandler)
	if ctx.Err() != nil {
		return ctx.Err()
	}

	// Preserve streams and prioritize diagnostics when they compete for the
	// shared command-output budget. The fixed omission notice is outside it.
	errText, outText := stderr.text(), stdout.text()
	mode := "head"
	if options.tail {
		mode = "tail"
	}
	selectedOut, selectedErr := outText, errText
	if options.maxTokens > 0 {
		// Keep exact token selection with the readers' bundled tokenizer, in an
		// invocation the existing process-group owner can cancel during formatting.
		formatted, err := toolplugin.Execute(ctx, manifest.NodeExecutable, runtimeRoot,
			shellContribution.Module, shellContribution.ModuleIndex,
			[]string{"--hrun-output", strconv.Itoa(options.maxTokens), mode, outText, errText},
			nil, handler.Dir, shellEnvironment(handler.Env))
		if err != nil {
			return fmt.Errorf("hrun: select output: %w", err)
		}
		if formatted.ExitCode != 0 {
			return fmt.Errorf("hrun: output selection failed")
		}
		selectedOut, selectedErr = formatted.Stdout, formatted.Stderr
	}
	if _, err := io.WriteString(handler.Stdout, selectedOut); err != nil {
		return err
	}
	if _, err := io.WriteString(handler.Stderr, selectedErr); err != nil {
		return err
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
		if _, err := fmt.Fprintf(handler.Stderr, "%shrun: output incomplete: %s reached\n", separator, limit); err != nil {
			return err
		}
	}
	return runErr
}
