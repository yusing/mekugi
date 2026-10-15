package read

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/yusing/mekugi/libraries/commandargs"
)

const readBundleUsage = "mcat [-n N] [--max-tokens N] [--tail] [--number] PATH [START:END ...] [PATH [START:END ...] ...]"
const maxReadBundleReads = 16

// Selection is one source and its normalized inclusive range, if present.
type Selection struct {
	Path       string
	Range      string
	start, end int
}

// Parse validates argv and expands source selections without reading content.
func (Service) Parse(args []string) ([]Selection, int, error) {
	selections, options, err := parse(args)
	if err != nil {
		return nil, 0, err
	}
	return selections, options.Budget, nil
}

func parse(args []string) ([]Selection, commandargs.ReaderOptions, error) {
	options, rest, err := commandargs.ParseReaderOptions(args, true, 6000, "")
	if err != nil {
		return nil, commandargs.ReaderOptions{}, err
	}
	var operands []string
	ended := false
	for _, arg := range rest {
		if !ended && arg == "--" {
			ended = true
			continue
		}
		if !ended && strings.HasPrefix(arg, "-") {
			return nil, commandargs.ReaderOptions{}, fmt.Errorf("unknown option %q", arg)
		}
		operands = append(operands, arg)
	}
	var specs []Selection
	for _, operand := range operands {
		if commandargs.LineRangePattern.MatchString(operand) {
			if len(specs) == 0 {
				return nil, commandargs.ReaderOptions{}, fmt.Errorf("range-like paths require ./; retry: %s", retryCommand("./"+operand))
			}
			operand = strings.ReplaceAll(operand, "-", ":")
			start, end, err := commandargs.ParseLineRange(operand)
			if err != nil || start > 1<<53-1 || end > 1<<53-1 {
				return nil, commandargs.ReaderOptions{}, errors.New("line range must be START:END with canonical safe integers and START not after END")
			}
			if specs[len(specs)-1].Range == "" {
				specs[len(specs)-1].Range = operand
				specs[len(specs)-1].start, specs[len(specs)-1].end = max(1, start), end
			} else {
				specs = append(specs, Selection{Path: specs[len(specs)-1].Path, Range: operand, start: max(1, start), end: end})
			}
			continue
		}
		if correction := correctedReadRange(operand); correction != "" {
			path := "PATH"
			if len(specs) > 0 {
				path = specs[len(specs)-1].Path
			}
			return nil, commandargs.ReaderOptions{}, fmt.Errorf("invalid range %q; retry: %s", operand, retryCommand(path, correction))
		}
		if path, row, ok := strings.CutLast(operand, ":"); ok && path != "" && readRowNumber.MatchString(row) {
			if _, err := os.Stat(operand); errors.Is(err, os.ErrNotExist) {
				return nil, commandargs.ReaderOptions{}, fmt.Errorf("ranges must be separate operands; retry: %s", retryCommand(path, row+":"+row))
			}
		}
		if operand == "" {
			return nil, commandargs.ReaderOptions{}, errors.New(readBundleUsage + " (1–16 reads)")
		}
		specs = append(specs, Selection{Path: operand})
	}
	if len(specs) == 0 {
		return nil, commandargs.ReaderOptions{}, errors.New(readBundleUsage)
	}
	if len(specs) > maxReadBundleReads {
		calls := (len(specs) + maxReadBundleReads - 1) / maxReadBundleReads
		return nil, commandargs.ReaderOptions{}, fmt.Errorf("received %d reads after argument expansion, exceeding the %d-read limit; split into %d mcat calls of at most %d reads each; repeat the path when splitting its ranges", len(specs), maxReadBundleReads, calls, maxReadBundleReads)
	}
	if len(specs) > 1 && (options.Lines != 0 || options.Tail) {
		return nil, commandargs.ReaderOptions{}, errors.New("-n and --tail require a single path")
	}
	return specs, options, nil
}

var readRowNumber = regexp.MustCompile(`^[0-9]+$`)
var readMalformedRange = regexp.MustCompile(`^([0-9]+)(:\+|,)([0-9]+)$`)

func correctedReadRange(operand string) string {
	if readRowNumber.MatchString(operand) {
		return operand + ":" + operand
	}
	parts := readMalformedRange.FindStringSubmatch(operand)
	if parts == nil {
		return ""
	}
	if parts[2] == "," {
		return parts[1] + ":" + parts[3]
	}
	start, err := strconv.ParseUint(parts[1], 10, 64)
	count, countErr := strconv.ParseUint(parts[3], 10, 64)
	if err != nil || countErr != nil || count == 0 || start > 1<<53-1 || count > 1<<53-max(1, start) {
		return parts[1] + ":END"
	}
	return fmt.Sprintf("%d:%d", max(1, start), max(1, start)+count-1)
}
