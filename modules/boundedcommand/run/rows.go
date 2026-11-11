package run

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"unicode/utf8"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/libraries/outputbudget"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
)

// Row-only capture has no implicit byte window. Spools belong to Execute, never
// to the delivery plan. Tail disk usage is proportional to emitted output; capture heap
// is independent of row size, row count and total output, including finalization.
type mrunRowCapture struct {
	file          *os.File
	size          int64
	lines, limit  int
	tail, omitted bool
	err           error
}

func newMRunRowCapture(limit int, tail bool) (*mrunRowCapture, error) {
	file, err := os.CreateTemp("", "mekugi-mrun-*")
	if err != nil {
		return nil, fmt.Errorf("create row capture: %w", err)
	}
	return &mrunRowCapture{file: file, limit: limit, tail: tail}, nil
}

func (c *mrunRowCapture) Write(value []byte) (int, error) {
	length := len(value)
	if !c.tail {
		end := 0
		for end < len(value) && c.lines < c.limit {
			n := bytes.IndexByte(value[end:], '\n')
			if n < 0 {
				end = len(value)
				break
			}
			end += n + 1
			c.lines++
		}
		c.omitted = c.omitted || end < len(value)
		value = value[:end]
	}
	if c.err == nil && len(value) > 0 {
		var n int
		n, c.err = c.file.Write(value)
		c.size += int64(n)
		if c.err == nil && n != len(value) {
			c.err = io.ErrShortWrite
		}
	}
	// A capture failure is reported after execution, not as EPIPE to the child.
	return length, nil
}

func (c *mrunRowCapture) close() error {
	closeErr := c.file.Close()
	removeErr := os.Remove(c.file.Name())
	if removeErr != nil {
		removeErr = fmt.Errorf("unreleased capture %s: %w", c.file.Name(), removeErr)
	}
	return errors.Join(closeErr, removeErr)
}

func executeMRunRows(ctx context.Context, child *exec.Cmd, lines int, tail bool) (execution command.Output, err error) {
	var captures []*mrunRowCapture
	defer func() {
		var cleanup error
		for _, capture := range captures {
			cleanup = errors.Join(cleanup, capture.close())
		}
		if cleanup == nil {
			return
		}
		if err != nil {
			err = errors.Join(err, cleanup)
			return
		}
		// Finalized strings are still valid. Cleanup failure must not discard
		// admitted output or prevent independent recovery publication.
		appendMRunNotice(&execution, fmt.Sprintf("mrun: capture cleanup failed: %v\n", cleanup))
		if execution.ExitCode == 0 {
			execution.ExitCode = 1
		}
	}()
	for range 2 {
		capture, createErr := newMRunRowCapture(lines, tail)
		if createErr != nil {
			return command.Output{}, createErr
		}
		captures = append(captures, capture)
	}
	stdout, stderr := captures[0], captures[1]
	child.Stdout, child.Stderr = stdout, stderr
	execution, err = runMRunChild(ctx, child)
	if err != nil || execution.Stderr != "" {
		return execution, err
	}
	if err := errors.Join(stdout.err, stderr.err); err != nil {
		return command.Output{}, fmt.Errorf("capture command rows: %w", err)
	}
	return finalizeMRunRows(ctx, stdout, stderr, execution.ExitCode)
}

// Keep a full delivery byte ceiling plus lookahead. A bounded candidate must
// never masquerade as a complete unterminated stream to rows-mode selection.
const mrunRowPrefixBytes = mrunDeliveryTokens*4 + utf8.UTFMax

type mrunSelectedRows struct {
	capture                *mrunRowCapture
	start, normalizedBytes int64
	prefix                 string
}

func (c *mrunRowCapture) selectRows(ctx context.Context) (mrunSelectedRows, error) {
	selected := mrunSelectedRows{capture: c}
	if c.tail {
		buffer := make([]byte, 32<<10)
		remaining := c.limit
		for end := c.size; end > 0 && remaining > 0; {
			if err := ctx.Err(); err != nil {
				return selected, err
			}
			start := max(int64(0), end-int64(len(buffer)))
			n, err := c.file.ReadAt(buffer[:end-start], start)
			if err != nil {
				return selected, err
			}
			for i := n - 1; i >= 0; i-- {
				if buffer[i] == '\n' && start+int64(i) != c.size-1 {
					remaining--
					if remaining == 0 {
						selected.start = start + int64(i) + 1
						break
					}
				}
			}
			end = start
		}
		c.omitted = selected.start > 0
	}
	prefix := make([]byte, 0, mrunRowPrefixBytes)
	err := selected.normalize(ctx, func(part []byte) error {
		selected.normalizedBytes += int64(len(part))
		prefix = append(prefix, part[:min(len(part), cap(prefix)-len(prefix))]...)
		return nil
	})
	// A cutoff may split a valid rune; retain only the complete prefix. The
	// lookahead above still leaves more than any admissible delivery page.
	for len(prefix) > 0 && !utf8.Valid(prefix) {
		prefix = prefix[:len(prefix)-1]
	}
	selected.prefix = string(prefix)
	return selected, err
}

// Normalize exactly as strings.ToValidUTF8: preserve valid split runes and
// replace a consecutive invalid-byte run once, even across input chunks.
func (s mrunSelectedRows) normalize(ctx context.Context, emit func([]byte) error) error {
	reader := io.NewSectionReader(s.capture.file, s.start, s.capture.size-s.start)
	buffer := make([]byte, (32<<10)+utf8.UTFMax)
	carry, invalid := 0, false
	replacement := []byte("\uFFFD")
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := reader.Read(buffer[carry:])
		if err != nil && err != io.EOF {
			return err
		}
		value := buffer[:carry+n]
		start, i := 0, 0
		for i < len(value) {
			if value[i] < utf8.RuneSelf {
				invalid = false
				i++
				continue
			}
			if err != io.EOF && !utf8.FullRune(value[i:]) {
				break
			}
			r, size := utf8.DecodeRune(value[i:])
			if r == utf8.RuneError && size == 1 {
				if e := emit(value[start:i]); e != nil {
					return e
				}
				if !invalid {
					if e := emit(replacement); e != nil {
						return e
					}
				}
				invalid = true
				i++
				start = i
				continue
			}
			invalid = false
			i += size
		}
		if e := emit(value[start:i]); e != nil {
			return e
		}
		carry = copy(buffer, value[i:])
		if err == io.EOF {
			return nil
		}
	}
}

func (s mrunSelectedRows) omitted(ctx context.Context, delivered int) (string, error) {
	var result strings.Builder
	result.Grow(int(s.normalizedBytes) - delivered)
	err := s.normalize(ctx, func(part []byte) error {
		skip := min(delivered, len(part))
		delivered -= skip
		result.Write(part[skip:])
		return nil
	})
	return result.String(), err
}

type mrunCapacityFailure struct{ err error }

func (mrunCapacityFailure) ReferenceCount() int { return 0 }
func (p mrunCapacityFailure) Prepare([]string) (command.PreparedDelivery, error) {
	return command.PreparedDelivery{}, p.err
}

func finalizeMRunRows(ctx context.Context, stdout, stderr *mrunRowCapture, exitCode int) (command.Output, error) {
	out, err := stdout.selectRows(ctx)
	if err != nil {
		return command.Output{}, fmt.Errorf("select stdout rows: %w", err)
	}
	errorsOut, err := stderr.selectRows(ctx)
	if err != nil {
		return command.Output{}, fmt.Errorf("select stderr rows: %w", err)
	}
	page := outputbudget.Selection{Stdout: out.prefix, Stderr: errorsOut.prefix}
	// Preserve the cold fast path: one source byte cannot exceed one token.
	// The complete normalized streams fit these prefixes when this proves fit.
	if out.normalizedBytes+errorsOut.normalizedBytes > mrunDeliveryTokens ||
		out.normalizedBytes > 0 && errorsOut.normalizedBytes > mrunDeliveryTokens/2 {
		page, err = outputbudget.Select(ctx, mrunDeliveryTokens, "rows", out.prefix, errorsOut.prefix)
		if err != nil {
			return command.Output{}, fmt.Errorf("select retained output: %w", err)
		}
	}
	execution := command.Output{Stdout: page.Stdout, Stderr: page.Stderr, ExitCode: exitCode}
	omittedBytes := out.normalizedBytes - int64(len(page.Stdout)) + errorsOut.normalizedBytes - int64(len(page.Stderr))
	if err := recovery.ValidateOutputSize(omittedBytes); err != nil {
		execution.Delivery = mrunCapacityFailure{err: err}
	} else if omittedBytes > 0 {
		omittedOut, err := out.omitted(ctx, len(page.Stdout))
		if err != nil {
			return command.Output{}, fmt.Errorf("finalize stdout rows: %w", err)
		}
		omittedErr, err := errorsOut.omitted(ctx, len(page.Stderr))
		if err != nil {
			return command.Output{}, fmt.Errorf("finalize stderr rows: %w", err)
		}
		execution.OmittedOutput = &command.OmittedOutput{
			Stdout: omittedOut, Stderr: omittedErr,
			StdoutKind: mrunRetainedKind(omittedOut), StderrKind: mrunRetainedKind(omittedErr),
		}
		if execution.Stderr != "" && !strings.HasSuffix(execution.Stderr, "\n") {
			execution.Stderr += "\n"
		}
	}
	if stdout.omitted || stderr.omitted {
		appendMRunNotice(&execution, fmt.Sprintf("mrun: output incomplete: %d-line limit reached\n", stdout.limit))
	}
	return execution, nil
}
