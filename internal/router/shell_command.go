package router

import (
	"strings"

	"github.com/yusing/mekugi/internal/shellsyntax"
)

func shellCommandString(executable string, arguments []string) string {
	var command strings.Builder
	command.WriteString(shellsyntax.QuoteArgument(executable))
	for _, argument := range arguments {
		command.WriteByte(' ')
		command.WriteString(shellsyntax.QuoteArgument(argument))
	}
	return command.String()
}
