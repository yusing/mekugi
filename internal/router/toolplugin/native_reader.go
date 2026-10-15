package toolplugin

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"

	"github.com/yusing/mekugi/libraries/outputbudget/tokenizer"
)

const nativeRetainedBytes = 16 << 20

// ExecuteBuiltin runs bundled readers in the authenticated frontend process.
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

type nativeBudget struct{ codec *tokenizer.Codec }

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

func (b *nativeBudget) selectRows(value string, limit int, tail bool) (string, error) {
	if len(value) <= limit {
		return value, nil
	}
	if _, err := b.count(""); err != nil {
		return "", err
	}
	return b.codec.SelectRows(value, limit, tail)
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
