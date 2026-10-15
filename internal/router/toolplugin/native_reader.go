package toolplugin

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"

	"github.com/yusing/mekugi/contracts/command"
)

const nativeRetainedBytes = 16 << 20

// ExecuteBuiltin runs bundled readers in the authenticated frontend process.
func ExecuteBuiltin(ctx context.Context, name string, args []string) (command.Output, error) {
	if err := ctx.Err(); err != nil {
		return command.Output{}, err
	}
	switch name {
	case "mcat":
		return nativeMCat(ctx, args)
	default:
		return command.Output{}, fmt.Errorf("native reader %q is unavailable", name)
	}
}

func nativeJSON(v any) string {
	b, err := json.Marshal(v, json.Deterministic(true), jsontext.PreserveRawStrings(true), jsontext.AllowInvalidUTF8(true))
	if err != nil {
		panic(err)
	}
	return string(b)
}
func nativeFailure(name string, err error, class string) command.Output {
	return command.Output{Stderr: name + ": " + err.Error() + "\n", ExitCode: 1, FailureClass: class}
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
