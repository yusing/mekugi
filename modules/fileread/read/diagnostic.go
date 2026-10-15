package read

import (
	"encoding/json/jsontext"
	"errors"
	"os"
	"strings"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/libraries/commandanalysis/shellsyntax"
)

func quotePath(path string) string {
	quoted, _ := jsontext.AppendQuote(nil, path)
	return string(quoted)
}

func retryCommand(args ...string) string {
	for i, arg := range args {
		args[i] = shellsyntax.QuoteArgument(arg)
	}
	return "mcat " + strings.Join(args, " ")
}

func failure(err error, class string) command.Output {
	return command.Output{Stderr: "mcat: " + err.Error() + "\n", ExitCode: 1, FailureClass: class}
}

func fileClass(err error) string {
	if errors.Is(err, os.ErrNotExist) {
		return "not_found"
	}
	if errors.Is(err, os.ErrPermission) {
		return "permission_denied"
	}
	return "reader_error"
}

// Bundles preserve JSON-safe JavaScript separators in their manifest paths.
func quoteBundlePath(path string) string {
	return strings.ReplaceAll(strings.ReplaceAll(quotePath(path), "\u2028", `\u2028`), "\u2029", `\u2029`)
}
