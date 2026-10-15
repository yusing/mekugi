package toolplugin

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/libraries/commandargs"
	"github.com/yusing/mekugi/libraries/outputbudget"

	"github.com/yusing/mekugi/libraries/sourceanalysis/sourceparse"
)

// Source: plugins/mcat.ts:166:310@[543de4f3] readLines
// Scan the entire source even after admission stops. Neither an oversized row
// nor omitted data can grow memory beyond the reader's recovery bounds.
func nativeMCat(ctx context.Context, args []string) (command.Output, error) {
	o, rest, err := commandargs.ParseReaderOptions(args, true, 6000, "")
	if err != nil {
		return nativeFailure("mcat", err, "invalid_arguments"), nil
	}
	if index := slices.Index(rest, "--"); index >= 0 {
		rest = slices.Delete(rest, index, index+1)
	}
	if len(rest) < 1 || len(rest) > 2 || rest[0] == "" {
		return nativeFailure("mcat", errors.New("mcat expected PATH or PATH START:END"), "invalid_arguments"), nil
	}
	start, end := 0, 0
	if len(rest) == 2 {
		start, end, err = commandargs.ParseLineRange(rest[1])
		if err != nil {
			return nativeFailure("mcat", err, "invalid_arguments"), nil
		}
		start = max(1, start)
	}
	path := rest[0]
	fail := func(err error) (command.Output, error) {
		class := nativeFileClass(err)
		message := err.Error()
		switch {
		case errors.Is(err, syscall.ENOENT):
			message = "ENOENT: no such file or directory"
		case errors.Is(err, syscall.ENOTDIR):
			message = "ENOTDIR: not a directory"
		case errors.Is(err, os.ErrPermission):
			message = "EACCES: permission denied"
		}
		return nativeFailure("mcat", fmt.Errorf("%s: %s", nativeJSON(path), message), class), nil
	}
	// Check before opening to avoid waiting on a FIFO. Recheck the opened handle.
	info, err := os.Stat(path)
	if err != nil {
		return fail(err)
	}
	if !info.Mode().IsRegular() {
		return fail(errors.New("not a regular file"))
	}
	f, err := sourceparse.Open(path)
	if err != nil {
		return fail(err)
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return fail(err)
	}
	if !info.Mode().IsRegular() {
		return fail(errors.New("not a regular file"))
	}
	r := bufio.NewReaderSize(f, 32*1024)
	var row, shown, retained strings.Builder
	var tailRows []string
	tailBytes, tailHead := 0, 0
	line, selected := 1, 0
	incomplete, unavailable, oversized, open, pendingCR := false, false, false, false, false
	reason := ""
	budget := outputbudget.Budget{}
	finish := func() {
		if (start == 0 || line >= start && line <= end) && !oversized {
			selected++
			s := row.String() + "\n"
			if o.Number {
				s = fmt.Sprintf("%6d\t%s", line, s)
			}
			if !unavailable {
				if retained.Len()+len(s) > nativeRetainedBytes {
					unavailable = true
					retained.Reset()
					reason = "result exceeds the 16 MiB recovery bound; narrow the source range\n"
				} else {
					retained.WriteString(s)
				}
			}
			if o.Tail {
				tailRows = append(tailRows, s)
				tailBytes += len(s)
				for (o.Lines > 0 && len(tailRows)-tailHead > o.Lines) || tailBytes > o.Budget*128 {
					tailBytes -= len(tailRows[tailHead])
					tailRows[tailHead] = ""
					tailHead++
					incomplete = true
				}
				if tailHead > 1024 {
					tailRows = append([]string(nil), tailRows[tailHead:]...)
					tailHead = 0
				}
			} else if !incomplete {
				if o.Lines > 0 && selected > o.Lines {
					incomplete = true
				} else if shown.Len()+len(s) > o.Budget*128 {
					incomplete = true
				} else {
					shown.WriteString(s)
				}
			}
		}
		row.Reset()
		line++
		open = false
		oversized = false
	}
	for {
		if err := ctx.Err(); err != nil {
			return command.Output{}, err
		}
		ch, size, e := r.ReadRune()
		if e == io.EOF {
			break
		}
		if e != nil {
			return fail(e)
		}
		if ch == utf8.RuneError && size == 1 {
			return fail(errors.New("not UTF-8"))
		}
		if pendingCR {
			pendingCR = false
			finish()
			if ch == '\n' {
				continue
			}
		}
		if ch == '\r' {
			pendingCR = true
			open = true
			continue
		}
		if ch == '\n' {
			finish()
			continue
		}
		open = true
		if (start != 0 && (line < start || line > end)) || oversized || unavailable && !o.Tail && incomplete {
			continue
		}
		if row.Len()+size > commandargs.MaxOutputTokens*128 {
			oversized = true
			unavailable = true
			retained.Reset()
			row.Reset()
			incomplete = true
			reason = fmt.Sprintf("row %d exceeds the %d-byte inspection bound; use mrun with a byte-oriented command such as head -c\n", line, commandargs.MaxOutputTokens*128)
			if o.Tail {
				tailRows = nil
				tailHead = 0
				tailBytes = 0
			}
			continue
		}
		row.WriteRune(ch)
	}
	if open || pendingCR {
		finish()
	}
	if start > line-1 {
		return fail(fmt.Errorf("rows %d:%d past EOF (%d rows)", start, end, line-1))
	}
	current := shown.String()
	if o.Tail {
		current = strings.Join(tailRows[tailHead:], "")
	}
	bounded, err := budget.SelectRows(current, o.Budget, o.Tail)
	if err != nil {
		return command.Output{}, err
	}
	incomplete = incomplete || len(bounded) < len(current)
	current = bounded
	result := command.Output{Stdout: current}
	if incomplete {
		result.ExitCode = 1
		result.FailureClass = "output_limit"
		if reason == "" {
			reason = fmt.Sprintf("output incomplete: %d-token limit reached\n", o.Budget)
			if o.Lines > 0 {
				reason = fmt.Sprintf("output incomplete: %d-line limit or %d-token limit reached\n", o.Lines, o.Budget)
			}
		}
		result.Stderr = "mcat: " + reason
		if !unavailable {
			all := retained.String()
			omitted := strings.TrimPrefix(all, current)
			if o.Tail {
				omitted = strings.TrimSuffix(all, current)
			}
			result.OmittedOutput = &command.OmittedOutput{Stdout: omitted, StdoutKind: "rows"}
		}
	}
	return result, nil
}
