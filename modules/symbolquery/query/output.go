package query

import (
	"encoding/json/jsontext"

	"github.com/yusing/mekugi/contracts/command"
)

const retainedBytes = 16 << 20

func quotePath(path string) string {
	// AppendQuote returns replacement characters even when reporting invalid UTF-8.
	quoted, _ := jsontext.AppendQuote(nil, path)
	return string(quoted)
}
func failure(err error, class string) command.Output {
	return command.Output{Stderr: "msymbol: " + err.Error() + "\n", ExitCode: 1, FailureClass: class}
}
