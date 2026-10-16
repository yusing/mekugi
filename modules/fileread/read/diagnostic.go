package read

import (
	"encoding/json/jsontext"
	"errors"
	"os"
	"strings"

	"github.com/yusing/mekugi/contracts/command"
)

func quotePath(path string) string {
	quoted, _ := jsontext.AppendQuote(nil, path)
	return string(quoted)
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
