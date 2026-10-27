package read

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/libraries/commandargs"
	"github.com/yusing/mekugi/libraries/sourceanalysis/sourceparse"
)

const retainedBytes = 16 << 20

func scan(ctx context.Context, selection commandargs.ReadSelection, o commandargs.ReaderOptions) (command.Output, error) {
	outputs, err := scanSelections(ctx, []commandargs.ReadSelection{selection}, o)
	if err != nil {
		return command.Output{}, err
	}
	return outputs[0], nil
}

// scanSelections acquires one source for selections with the exact same path.
func scanSelections(ctx context.Context, selections []commandargs.ReadSelection, o commandargs.ReaderOptions) ([]command.Output, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := selections[0].Path
	fail := func(err error) ([]command.Output, error) {
		return sourceFailures(selections, err), nil
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
	return scanRows(ctx, f, selections, o)
}

// Source: plugins/mcat.ts:166:310@[543de4f3] readLines
// Scan and validate the entire source once, sharing each selected logical row
// while keeping admission and recovery bounds independent for every selection.
func scanRows(ctx context.Context, source io.Reader, selections []commandargs.ReadSelection, o commandargs.ReaderOptions) ([]command.Output, error) {
	r := bufio.NewReaderSize(source, 32*1024)
	states := make([]selectionScan, len(selections))
	for i, selection := range selections {
		states[i].selection = selection
	}
	var row strings.Builder
	line := 1
	oversized, open, pendingCR := false, false, false
	wanted := make([]*selectionScan, 0, len(states))
	selectRow := func() {
		wanted = wanted[:0]
		for i := range states {
			if states[i].wants(line, o) {
				wanted = append(wanted, &states[i])
			}
		}
	}
	selectRow()
	finish := func() {
		if oversized {
			for _, state := range wanted {
				state.rejectOversized(line, o)
			}
		} else if len(wanted) > 0 {
			text := row.String() + "\n"
			if o.Number {
				text = fmt.Sprintf("%6d\t%s", line, text)
			}
			for _, state := range wanted {
				state.accept(text, o)
			}
		}
		row.Reset()
		line++
		open = false
		oversized = false
		selectRow()
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ch, size, err := r.ReadRune()
		if err == io.EOF {
			break
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return sourceFailures(selections, err), nil
		}
		if ch == utf8.RuneError && size == 1 {
			return sourceFailures(selections, errors.New("not UTF-8")), nil
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
		if len(wanted) == 0 || oversized {
			continue
		}
		if row.Len()+size > commandargs.MaxOutputTokens*128 {
			oversized = true
			row.Reset()
			continue
		}
		row.WriteRune(ch)
	}
	if open || pendingCR {
		finish()
	}
	outputs := make([]command.Output, len(states))
	for i := range states {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		state := &states[i]
		selection := state.selection
		if selection.Start > line-1 {
			outputs[i] = sourceFailure(selection.Path, fmt.Errorf("rows %d:%d past EOF (%d rows)", selection.Start, selection.End, line-1))
			continue
		}
		output, err := state.output(o)
		if err != nil {
			return nil, err
		}
		outputs[i] = output
	}
	return outputs, nil
}

func sourceFailures(selections []commandargs.ReadSelection, err error) []command.Output {
	outputs := make([]command.Output, len(selections))
	for i, selection := range selections {
		outputs[i] = sourceFailure(selection.Path, err)
	}
	return outputs
}

func sourceFailure(path string, err error) command.Output {
	message := err.Error()
	switch {
	case errors.Is(err, syscall.ENOENT):
		message = "ENOENT: no such file or directory"
	case errors.Is(err, syscall.ENOTDIR):
		message = "ENOTDIR: not a directory"
	case errors.Is(err, os.ErrPermission):
		message = "EACCES: permission denied"
	}
	return failure(fmt.Errorf("%s: %s", quotePath(path), message), fileClass(err))
}
