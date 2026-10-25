package commandargs

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// RunOptions selects the bounded foreground command output.
type RunOptions struct {
	Lines  int
	Tokens int
	Tail   bool
}

// ParseRunOptions separates mrun options from the untouched child argv.
func ParseRunOptions(arguments []string) (RunOptions, []string, error) {
	var options RunOptions
	for len(arguments) > 0 {
		arguments = ExpandMaxTokensOption(arguments)
		switch arguments[0] {
		case "--max-tokens":
			if len(arguments) < 2 || options.Tokens != 0 {
				return options, nil, errors.New(MaxTokensArgumentError)
			}
			value := arguments[1]
			number, err := ParseMaxTokens(value)
			if err != nil {
				return options, nil, err
			}
			options.Tokens = number
			arguments = arguments[2:]
		case "-n":
			if len(arguments) < 2 || options.Lines != 0 {
				return options, nil, fmt.Errorf("-n requires one positive integer and cannot repeat")
			}
			number, err := strconv.Atoi(arguments[1])
			if err != nil || number < 1 || strconv.Itoa(number) != arguments[1] {
				return options, nil, fmt.Errorf("-n requires one positive integer")
			}
			options.Lines = number
			arguments = arguments[2:]

		case "--tail":
			if options.Tail {
				return options, nil, fmt.Errorf("--tail cannot repeat")
			}
			options.Tail = true
			arguments = arguments[1:]
		case "--":
			if (options.Tokens == 0 && options.Lines == 0) || len(arguments) < 2 || arguments[1] == "" {
				return options, nil, fmt.Errorf("expected -n N or --max-tokens N, optionally --tail, then -- COMMAND [ARG...]")
			}
			return options, arguments[1:], nil
		default:
			if !strings.HasPrefix(arguments[0], "-") && arguments[0] != "" && (options.Tokens != 0 || options.Lines != 0) {
				return options, arguments, nil
			}
			return options, nil, fmt.Errorf("expected -n N or --max-tokens N, optionally --tail, then -- COMMAND [ARG...]")
		}
	}
	return options, nil, fmt.Errorf("expected -n N or --max-tokens N, optionally --tail, then -- COMMAND [ARG...]")
}
