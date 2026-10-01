package toolplugin

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/yusing/mekugi/internal/tokenizer"
)

const nativeMaxTokens = 15500
const nativeRetainedBytes = 16 << 20

// ExecuteBuiltin runs bundled readers in the authenticated frontend process.
// Configured plugins remain isolated JavaScript hosts, never native executors.
func ExecuteBuiltin(ctx context.Context, name string, args []string) (ExecutionOutput, error) {
	if err := ctx.Err(); err != nil {
		return ExecutionOutput{}, err
	}
	switch name {
	case "mcat":
		return nativeMCat(ctx, args)
	case "inspect_file":
		return nativeInspect(ctx, args)
	case "msymbol":
		return nativeSymbol(ctx, args)
	default:
		return ExecutionOutput{}, fmt.Errorf("native reader %q is unavailable", name)
	}
}

type readerOptions struct {
	budget, lines int
	tail, number  bool
}

// Source: plugins/common.ts:48:99@[543de4f3] readerOptions
func nativeReaderOptions(args []string, allowTail bool, defaultBudget int, takesValue string) (readerOptions, []string, error) {
	o := readerOptions{budget: defaultBudget}
	rest := []string{}
	seenBudget := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		if a == "--max-tokens" || strings.HasPrefix(a, "--max-tokens=") || (allowTail && a == "-n") {
			name := a
			raw := ""
			if v, ok := strings.CutPrefix(a, "--max-tokens="); ok {
				name = "--max-tokens"
				raw = v
			} else if i+1 < len(args) {
				i++
				raw = args[i]
			}
			maximum := nativeMaxTokens
			repeated := seenBudget
			if name == "-n" {
				maximum = 9007199254740991
				repeated = o.lines != 0
			}
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > maximum || raw != strconv.Itoa(n) || repeated {
				return o, nil, fmt.Errorf("%s requires one integer from 1 to %d and cannot repeat", name, maximum)
			}
			if name == "-n" {
				o.lines = n
			} else {
				o.budget = n
				seenBudget = true
			}
			continue
		}
		if allowTail && (a == "--tail" || a == "--number") {
			p := &o.tail
			if a == "--number" {
				p = &o.number
			}
			if *p {
				return o, nil, fmt.Errorf("%s cannot repeat", a)
			}
			*p = true
			continue
		}
		rest = append(rest, a)
		if a == takesValue && i+1 < len(args) {
			i++
			rest = append(rest, args[i])
		}
	}
	if o.tail && o.lines == 0 && !seenBudget {
		return o, nil, errors.New("--tail requires -n or --max-tokens")
	}
	return o, rest, nil
}

type nativeBudget struct{ codec tokenizer.Codec }

func (b *nativeBudget) count(s string) (int, error) {
	if b.codec == nil {
		var err error
		b.codec, err = tokenizer.New()
		if err != nil {
			return 0, err
		}
	}
	return b.codec.Count(s)
}
func (b *nativeBudget) fits(s string, limit int) (bool, error) {
	if len(s) <= limit {
		return true, nil
	}
	n, e := b.count(s)
	return n <= limit, e
}
func nativeJSON(v any) string {
	b, err := json.Marshal(v, json.Deterministic(true), jsontext.PreserveRawStrings(true), jsontext.AllowInvalidUTF8(true))
	if err != nil {
		panic(err)
	}
	return string(b)
}
func nativeFailure(name string, err error, class string) ExecutionOutput {
	return ExecutionOutput{Stderr: name + ": " + err.Error() + "\n", ExitCode: 1, FailureClass: class}
}
func nativeFileClass(err error) string {
	if errors.Is(err, os.ErrNotExist) {
		return "not_found"
	}
	if errors.Is(err, os.ErrPermission) {
		return "permission_denied"
	}
	return "reader_error"
}
