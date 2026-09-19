package router

import (
	"context"
	"fmt"
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

// executeShellPrintf implements Bash's %q conversion without evaluating the
// resulting text. All other formatting remains owned by mvdan's formatter.
func executeShellPrintf(ctx context.Context, args []string) error {
	h := interp.HandlerCtx(ctx)
	if len(args) < 2 {
		return h.Builtin(ctx, args)
	}

	format, quoteArgs := printfQuoteFormat(args[1])
	if len(quoteArgs) == 0 {
		return h.Builtin(ctx, args)
	}
	formatArgs := append([]string(nil), args[2:]...)
	for offset := 0; ; {
		remaining := len(formatArgs) - offset
		cycleArgs := formatArgs[offset:]
		for _, index := range quoteArgs {
			for len(cycleArgs) <= index {
				cycleArgs = append(cycleArgs, "")
			}
			quoted, err := syntax.Quote(cycleArgs[index], syntax.LangBash)
			if err != nil {
				fmt.Fprintf(h.Stderr, "printf: %v\n", err)
				return interp.ExitStatus(1)
			}
			cycleArgs[index] = quoted
		}
		text, consumed, err := expand.Format(nil, format, cycleArgs)
		if err != nil {
			fmt.Fprintf(h.Stderr, "printf: %v\n", err)
			return interp.ExitStatus(1)
		}
		if _, err := fmt.Fprint(h.Stdout, text); err != nil {
			return err
		}
		if consumed == 0 || consumed >= remaining {
			break
		}
		offset += consumed
	}
	return nil
}

// printfQuoteFormat changes %q to %s and returns the zero-based argument
// positions consumed by those conversions during one format pass.
func printfQuoteFormat(format string) (string, []int) {
	var converted strings.Builder
	converted.Grow(len(format))
	var quoteArgs []int
	consumed := 0

	for i := 0; i < len(format); i++ {
		if format[i] == '\\' && i+1 < len(format) {
			converted.WriteByte(format[i])
			i++
			converted.WriteByte(format[i])
			continue
		}
		if format[i] != '%' {
			converted.WriteByte(format[i])
			continue
		}

		start := i
		i++
		for i < len(format) && (format[i] == '+' || format[i] == '-' || format[i] == ' ' ||
			format[i] >= '0' && format[i] <= '9') {
			i++
		}
		if i >= len(format) {
			converted.WriteString(format[start:])
			break
		}
		converted.WriteString(format[start:i])
		conversion := format[i]
		if conversion == 'q' {
			converted.WriteByte('s')
			quoteArgs = append(quoteArgs, consumed)
			consumed++
		} else {
			converted.WriteByte(conversion)
			if conversion != '%' {
				consumed++
			}
		}
	}
	return converted.String(), quoteArgs
}
