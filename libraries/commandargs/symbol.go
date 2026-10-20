package commandargs

// Source: modules/symbolquery/query/query.go:58:168@0a356401.

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// SymbolQuery selects one semantic definition or reference lookup.
type SymbolQuery struct {
	Mode, Path, Name string
	Line, Occurrence int
}

// SymbolArguments retains ordered selectors and explicit workspace selection.
// Parsing does not resolve the directory or grant access to it.
type SymbolArguments struct {
	Queries           []SymbolQuery
	Workspace         string
	ExplicitWorkspace bool
	Budget            int
}

const symbolUsage = "msymbol [--max-tokens N] [--workspace ROOT] (def|refs) PATH [LINE] SYMBOL [N] [(def|refs) PATH [LINE] SYMBOL [N] ...]"

var symbolDigits = regexp.MustCompile(`^[0-9]+$`)
var combinedSymbolPath = regexp.MustCompile(`^(.*):([1-9][0-9]*)$`)

func positiveSymbolInteger(s, label string) (int, error) {
	n, e := strconv.Atoi(s)
	if e != nil || n > 9007199254740991 {
		return 0, fmt.Errorf("%s is too large", label)
	}
	if n < 1 || s != strconv.Itoa(n) {
		return 0, fmt.Errorf("%s must be a positive decimal integer", label)
	}
	return n, nil
}

// ParseSymbolQueries validates a complete msymbol invocation without reading
// sources or starting a resolver. Execution and source display share this grammar.
func ParseSymbolQueries(args []string) (SymbolArguments, error) {
	o, rest, err := ParseReaderOptions(args, false, 4000, "--workspace")
	if err != nil {
		return SymbolArguments{}, err
	}
	var operands []string
	workspace := ""
	explicit, ended := false, false
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		if a == "--" && !ended {
			ended = true
			continue
		}
		if a == "--workspace" && !ended {
			if explicit || i+1 >= len(rest) || rest[i+1] == "" || strings.ContainsRune(rest[i+1], 0) {
				return SymbolArguments{}, errors.New("--workspace requires one usable directory")
			}
			explicit = true
			i++
			workspace = rest[i]
			continue
		}
		operands = append(operands, a)
	}
	var queries []SymbolQuery
	for i := 0; i < len(operands); {
		q := SymbolQuery{Mode: operands[i]}
		i++
		if q.Mode != "def" && q.Mode != "refs" {
			return SymbolArguments{}, errors.New("mode must be def or refs")
		}
		if i >= len(operands) || operands[i] == "" || strings.ContainsRune(operands[i], 0) {
			return SymbolArguments{}, errors.New("path must be usable")
		}
		q.Path = operands[i]
		i++
		if match := combinedSymbolPath.FindStringSubmatch(q.Path); match != nil {
			q.Path = match[1]
			if strings.HasPrefix(q.Path, "\"") {
				if json.Unmarshal([]byte(q.Path), &q.Path) != nil {
					return SymbolArguments{}, errors.New("invalid quoted PATH:LINE")
				}
			}
			q.Line, err = positiveSymbolInteger(match[2], "line")
		} else if i < len(operands) && symbolDigits.MatchString(operands[i]) {
			q.Line, err = positiveSymbolInteger(operands[i], "line")
			i++
		}
		if err != nil {
			return SymbolArguments{}, err
		}
		if i >= len(operands) {
			return SymbolArguments{}, fmt.Errorf("usage: %s", symbolUsage)
		}
		q.Name = operands[i]
		i++
		if _, last, ok := strings.CutLast(q.Name, "."); ok {
			q.Name = last
		}
		if q.Name == "" {
			return SymbolArguments{}, errors.New("SYMBOL must end with a usable name")
		}
		if i < len(operands) && symbolDigits.MatchString(operands[i]) {
			q.Occurrence, err = positiveSymbolInteger(operands[i], "N")
			i++
			if err != nil {
				return SymbolArguments{}, err
			}
			if q.Line == 0 {
				return SymbolArguments{}, errors.New("N requires an explicit LINE")
			}
		}
		queries = append(queries, q)
	}
	if len(queries) == 0 {
		return SymbolArguments{}, fmt.Errorf("usage: %s", symbolUsage)
	}
	return SymbolArguments{Queries: queries, Workspace: workspace, ExplicitWorkspace: explicit, Budget: o.Budget}, nil
}
