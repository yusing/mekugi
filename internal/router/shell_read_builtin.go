package router

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

// executeShellRead adds Bash's delimiter and timeout options to mvdan's read.
// Invocations not using either option remain entirely owned by mvdan.
func executeShellRead(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] != "read" {
		return fmt.Errorf("read: invalid invocation")
	}
	opts, names, enhanced, err := parseShellRead(args[1:])
	if err != nil {
		fmt.Fprintln(interp.HandlerCtx(ctx).Stderr, err)
		return interp.ExitStatus(2)
	}
	if !enhanced {
		return interp.HandlerCtx(ctx).Builtin(ctx, args)
	}
	for _, name := range names {
		if !syntax.ValidName(name) {
			fmt.Fprintf(interp.HandlerCtx(ctx).Stderr, "read: invalid identifier %q\n", name)
			return interp.ExitStatus(2)
		}
	}
	h := interp.HandlerCtx(ctx)
	file, ok := h.Stdin.(*os.File)
	if !ok {
		fmt.Fprintln(h.Stderr, "read: -d and -t require file-backed stdin")
		return interp.ExitStatus(2)
	}
	ownedFile, err := shellReadOwnedFile(file)
	if err != nil {
		fmt.Fprintf(h.Stderr, "read: cannot isolate stdin: %v\n", err)
		return interp.ExitStatus(2)
	}
	defer ownedFile.Close()
	file = ownedFile
	if opts.timeout != nil && *opts.timeout == 0 {
		ready, err := shellReadReady(file)
		if err != nil {
			fmt.Fprintf(h.Stderr, "read: readiness check: %v\n", err)
			return interp.ExitStatus(1)
		}
		if !ready {
			return interp.ExitStatus(1)
		}
		// Bash's read -t 0 only tests readiness and does not assign or consume.
		return nil
	}

	var deadline time.Time
	if opts.timeout != nil {
		deadline = time.Now().Add(*opts.timeout)
	}
	if contextDeadline, ok := ctx.Deadline(); ok && (deadline.IsZero() || contextDeadline.Before(deadline)) {
		deadline = contextDeadline
	}
	if !deadline.IsZero() {
		if err := file.SetReadDeadline(deadline); err != nil {
			info, statErr := file.Stat()
			if statErr != nil || !info.Mode().IsRegular() {
				fmt.Fprintf(h.Stderr, "read: timeout unsupported: %v\n", err)
				return interp.ExitStatus(2)
			}
		}
	}
	cancelDone := make(chan struct{})
	stopCancel := context.AfterFunc(ctx, func() {
		_ = file.SetReadDeadline(time.Now())
		close(cancelDone)
	})
	// Wait for cancellation before closing the owned descriptor; its deadline
	// needs no reset because this invocation never reuses it.
	defer func() {
		if !stopCancel() {
			<-cancelDone
		}
	}()
	value, readErr := readShellDelimited(file, opts.delimiter, opts.raw)
	if err := assignShellRead(ctx, h.Env, names, value, opts.raw); err != nil {
		return err
	}
	if readErr != nil {
		if errors.Is(readErr, os.ErrDeadlineExceeded) {
			return interp.ExitStatus(142)
		}
		return interp.ExitStatus(1)
	}
	return nil
}

type shellReadOptions struct {
	raw       bool
	delimiter byte
	timeout   *time.Duration
}

func parseShellRead(args []string) (shellReadOptions, []string, bool, error) {
	opts := shellReadOptions{delimiter: '\n'}
	enhanced := shellReadHasEnhancedOption(args)
	if !enhanced {
		return opts, args, false, nil
	}
	for len(args) > 0 && strings.HasPrefix(args[0], "-") && args[0] != "-" {
		arg := args[0]
		args = args[1:]
		if arg == "--" {
			break
		}
		switch {
		case arg == "-r":
			opts.raw = true
		case strings.HasPrefix(arg, "-d"), strings.HasPrefix(arg, "-t"):
			option, value := arg[:2], arg[2:]
			if value == "" {
				if len(args) == 0 {
					return opts, nil, false, fmt.Errorf("read: %s: option requires an argument", option)
				}
				value, args = args[0], args[1:]
			}
			if option == "-d" {
				opts.delimiter = 0
				if value != "" {
					opts.delimiter = value[0]
				}
				continue
			}
			seconds, err := strconv.ParseFloat(value, 64)
			if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 || seconds > float64(math.MaxInt64)/float64(time.Second) {
				return opts, nil, false, fmt.Errorf("read: %s: invalid timeout", value)
			}
			opts.timeout = new(time.Duration(seconds * float64(time.Second)))
		default:
			return opts, nil, false, fmt.Errorf("read: unsupported option %q with -d or -t", arg)
		}
	}
	return opts, args, enhanced, nil
}
func shellReadHasEnhancedOption(args []string) bool {
	for len(args) > 0 {
		arg := args[0]
		args = args[1:]
		if arg == "--" || len(arg) < 2 || arg[0] != '-' {
			return false
		}
		if strings.HasPrefix(arg, "-d") || strings.HasPrefix(arg, "-t") {
			return true
		}
		// Native read -p consumes the next argument, including in grouped flags.
		if strings.ContainsRune(arg[1:], 'p') && len(args) > 0 {
			args = args[1:]
		}
	}
	return false
}

func readShellDelimited(file *os.File, delimiter byte, raw bool) ([]byte, error) {
	var value []byte
	escaped := false
	var one [1]byte
	for {
		n, err := file.Read(one[:])
		if n > 0 {
			b := one[0]
			switch {
			case b == 0 && delimiter != 0:
				continue
			case !raw && b == '\\':
				value = append(value, b)
				escaped = !escaped
			case !raw && b == '\n' && escaped:
				value = value[:len(value)-1]
				escaped = false
			case b == delimiter && (raw || !escaped):
				return value, nil
			default:
				value = append(value, b)
				escaped = false
			}
		}
		if err != nil {
			return value, err
		}
	}
}

func assignShellRead(ctx context.Context, env expand.Environ, names []string, value []byte, raw bool) error {
	if len(names) == 0 {
		names = []string{"REPLY"}
	} else {
		fields := expand.ReadFields(&expand.Config{Env: env}, string(value), len(names), raw)
		values := make([]string, len(names))
		copy(values, fields)
		return evalShellReadAssignments(ctx, names, values)
	}
	text := string(value)
	if !raw {
		var unescaped strings.Builder
		escaped := false
		for i := range len(text) {
			if text[i] == '\\' && !escaped {
				escaped = true
				continue
			}
			unescaped.WriteByte(text[i])
			escaped = false
		}
		text = unescaped.String()
	}
	return evalShellReadAssignments(ctx, names, []string{text})
}

func evalShellReadAssignments(ctx context.Context, names, values []string) error {
	var script strings.Builder
	for i, name := range names {
		quoted, err := syntax.Quote(values[i], syntax.LangBash)
		if err != nil {
			fmt.Fprintf(interp.HandlerCtx(ctx).Stderr, "read: assignment: %v\n", err)
			return interp.ExitStatus(1)
		}
		fmt.Fprintf(&script, "%s=%s\n", name, quoted)
	}
	return interp.HandlerCtx(ctx).Builtin(ctx, []string{"eval", script.String()})
}
