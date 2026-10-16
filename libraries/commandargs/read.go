// Source: modules/fileread/read/arguments.go:14:119@7197229d.
package commandargs

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

const readBundleUsage = "mcat [-n N] [--max-tokens N] [--tail] [--number] PATH [START:END ...] [PATH [START:END ...] ...]"
const maxReadBundleReads = 16

// ReadSelection is one source and its normalized inclusive range, if present.
type ReadSelection struct {
	Path       string
	Range      string
	Start, End int
}

// ParseReadBundle validates argv without reading content; ambiguous PATH:ROW
// operands use filesystem metadata to preserve existing literal filenames.
func ParseReadBundle(args []string) ([]ReadSelection, ReaderOptions, error) {
	options, rest, err := ParseReaderOptions(args, true, 6000, "")
	if err != nil {
		return nil, ReaderOptions{}, err
	}
	var operands []string
	ended := false
	for _, arg := range rest {
		if !ended && arg == "--" {
			ended = true
			continue
		}
		if !ended && strings.HasPrefix(arg, "-") {
			return nil, ReaderOptions{}, fmt.Errorf("unknown option %q", arg)
		}
		operands = append(operands, arg)
	}
	var specs []ReadSelection
	for _, operand := range operands {
		if LineRangePattern.MatchString(operand) {
			if len(specs) == 0 {
				return nil, ReaderOptions{}, fmt.Errorf("range-like paths require ./; retry: %s", retryCommand("./"+operand))
			}
			operand = strings.ReplaceAll(operand, "-", ":")
			start, end, err := ParseLineRange(operand)
			if err != nil || start > 1<<53-1 || end > 1<<53-1 {
				return nil, ReaderOptions{}, errors.New("line range must be START:END with canonical safe integers and START not after END")
			}
			if specs[len(specs)-1].Range == "" {
				specs[len(specs)-1].Range = operand
				specs[len(specs)-1].Start, specs[len(specs)-1].End = max(1, start), end
			} else {
				specs = append(specs, ReadSelection{Path: specs[len(specs)-1].Path, Range: operand, Start: max(1, start), End: end})
			}
			continue
		}
		if correction := correctedReadRange(operand); correction != "" {
			path := "PATH"
			if len(specs) > 0 {
				path = specs[len(specs)-1].Path
			}
			return nil, ReaderOptions{}, fmt.Errorf("invalid range %q; retry: %s", operand, retryCommand(path, correction))
		}
		if path, row, ok := strings.CutLast(operand, ":"); ok && path != "" && readRowNumber.MatchString(row) {
			if _, err := os.Stat(operand); errors.Is(err, os.ErrNotExist) {
				return nil, ReaderOptions{}, fmt.Errorf("ranges must be separate operands; retry: %s", retryCommand(path, row+":"+row))
			}
		}
		if operand == "" {
			return nil, ReaderOptions{}, errors.New(readBundleUsage + " (1–16 reads)")
		}
		specs = append(specs, ReadSelection{Path: operand})
	}
	if len(specs) == 0 {
		return nil, ReaderOptions{}, errors.New(readBundleUsage)
	}
	if len(specs) > maxReadBundleReads {
		calls := (len(specs) + maxReadBundleReads - 1) / maxReadBundleReads
		return nil, ReaderOptions{}, fmt.Errorf("received %d reads after argument expansion, exceeding the %d-read limit; split into %d mcat calls of at most %d reads each; repeat the path when splitting its ranges", len(specs), maxReadBundleReads, calls, maxReadBundleReads)
	}
	if len(specs) > 1 && (options.Lines != 0 || options.Tail) {
		return nil, ReaderOptions{}, errors.New("-n and --tail require a single path")
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

func retryCommand(args ...string) string {
	for i, arg := range args {
		args[i] = QuoteArgument(arg)
	}
	return "mcat " + strings.Join(args, " ")
}
