package router

import (
	"errors"
	"strconv"
	"strings"
)

const maxTokensArgumentError = "--max-tokens requires one integer from 1 to 15500 and cannot repeat"

func parseMaxTokens(value string) (int, error) {
	number, err := strconv.Atoi(value)
	if err != nil || number < 1 || number > maxOutputTokens || strconv.Itoa(number) != value {
		return 0, errors.New(maxTokensArgumentError)
	}
	return number, nil
}

// Expand only the next option, never operands owned by a command or -- boundary.
func expandMaxTokensOption(arguments []string) []string {
	if len(arguments) != 0 {
		if value, ok := strings.CutPrefix(arguments[0], "--max-tokens="); ok {
			return append([]string{"--max-tokens", value}, arguments[1:]...)
		}
	}
	return arguments
}
