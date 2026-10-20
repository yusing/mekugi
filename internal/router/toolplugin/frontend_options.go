package toolplugin

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const MaxOutputTokens = 15_500

const MaxTokensArgumentError = "--max-tokens requires one integer from 1 to 15500 and cannot repeat"

func ParseMaxTokens(value string) (int, error) {
	number, err := strconv.Atoi(value)
	if err != nil || number < 1 || number > MaxOutputTokens || strconv.Itoa(number) != value {
		return 0, errors.New(MaxTokensArgumentError)
	}
	return number, nil
}

// Expand only the next option, never operands owned by a command or -- boundary.
func ExpandMaxTokensOption(arguments []string) []string {
	if len(arguments) != 0 {
		if value, ok := strings.CutPrefix(arguments[0], "--max-tokens="); ok {
			return append([]string{"--max-tokens", value}, arguments[1:]...)
		}
	}
	return arguments
}

type ReaderOptions struct {
	Budget, Lines int
	Tail, Number  bool
}

// ParseReaderOptions removes shared reader options and retains the -- boundary
// and command-specific operands, including the value of takesValue.
// Source: plugins/common.ts:48:99@[543de4f3] readerOptions
func ParseReaderOptions(args []string, allowTail bool, defaultBudget int, takesValue string) (ReaderOptions, []string, error) {
	o := ReaderOptions{Budget: defaultBudget}
	rest := []string{}
	seenBudget := false
	for len(args) > 0 {
		args = ExpandMaxTokensOption(args)
		a := args[0]
		args = args[1:]
		if a == "--" {
			rest = append(rest, "--")
			rest = append(rest, args...)
			break
		}
		if a == "--max-tokens" || (allowTail && a == "-n") {
			name := a
			raw := ""
			if len(args) > 0 {
				raw = args[0]
				args = args[1:]
			}
			maximum := MaxOutputTokens
			repeated := seenBudget
			if name == "-n" {
				maximum = 9007199254740991
				repeated = o.Lines != 0
			}
			var n int
			var err error
			if name == "-n" {
				if LineRangePattern.MatchString(raw) {
					return o, nil, fmt.Errorf("-n takes a row count, not a range; retry: mcat PATH %s", strings.ReplaceAll(raw, "-", ":"))
				}
				n, err = strconv.Atoi(raw)
				if err == nil && (n < 1 || n > maximum || raw != strconv.Itoa(n)) {
					err = errors.New("invalid row count")
				}
			} else {
				n, err = ParseMaxTokens(raw)
			}
			if err != nil || repeated {
				return o, nil, fmt.Errorf("%s requires one integer from 1 to %d and cannot repeat", name, maximum)
			}
			if name == "-n" {
				o.Lines = n
			} else {
				o.Budget = n
				seenBudget = true
			}
			continue
		}
		if allowTail && (a == "--tail" || a == "--number") {
			p := &o.Tail
			if a == "--number" {
				p = &o.Number
			}
			if *p {
				return o, nil, fmt.Errorf("%s cannot repeat", a)
			}
			*p = true
			continue
		}
		rest = append(rest, a)
		if a == takesValue && len(args) > 0 {
			rest = append(rest, args[0])
			args = args[1:]
		}
	}
	if o.Tail && o.Lines == 0 && !seenBudget {
		return o, nil, errors.New("--tail requires -n or --max-tokens")
	}
	return o, rest, nil
}

// LineRangePattern identifies range operands before command-specific path handling.
var LineRangePattern = regexp.MustCompile(`^[0-9]+[:-][0-9]+$`)

// ParseLineRange parses canonical inclusive endpoints. Callers impose their own
// numeric ceiling; zero starts remain available for mcat's first-row convention.
func ParseLineRange(span string) (int, int, error) {
	first, last, ok := strings.Cut(strings.ReplaceAll(span, "-", ":"), ":")
	start, startErr := strconv.Atoi(first)
	end, endErr := strconv.Atoi(last)
	if !ok || startErr != nil || endErr != nil || start < 0 || end < 1 ||
		strconv.Itoa(start) != first || strconv.Itoa(end) != last || start > end {
		return 0, 0, errors.New("invalid inclusive line range")
	}
	return start, end, nil
}
