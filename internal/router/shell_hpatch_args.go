package router

import (
	"fmt"
	"io"
	"strconv"

	"github.com/yusing/mekugi"
	"mvdan.cc/sh/v3/interp"
)

func readHpatchStdin(handler *interp.HandlerContext) (string, error) {
	if handler.Stdin == nil {
		return "", nil
	}
	data, err := io.ReadAll(io.LimitReader(handler.Stdin, maxMekugiScriptBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxMekugiScriptBytes {
		return "", fmt.Errorf("script exceeds %d bytes", maxMekugiScriptBytes)
	}
	return string(data), nil
}

// parseHpatchEdits decodes the outer path/script argument contract. A lone path
// reads its pathless script from stdin; additional arguments must be complete
// PATH SCRIPT pairs. No generated source is inspected here beyond the explicit
// bounded script payload.
func parseHpatchEdits(handler *interp.HandlerContext, arguments []string) ([]mekugi.FileEdit, error) {
	if len(arguments) > 0 && arguments[0] == "--" {
		arguments = arguments[1:]
	}
	if len(arguments) == 0 {
		return nil, fmt.Errorf("usage: hpatch PATH [SCRIPT [PATH SCRIPT ...]]")
	}
	if len(arguments) == 1 {
		script, err := readHpatchStdin(handler)
		if err != nil {
			return nil, err
		}
		return []mekugi.FileEdit{{Path: arguments[0], Script: script}}, nil
	}
	if len(arguments)%2 != 0 {
		return nil, fmt.Errorf("usage: hpatch PATH [SCRIPT [PATH SCRIPT ...]]")
	}
	edits := make([]mekugi.FileEdit, 0, len(arguments)/2)
	total := 0
	for index := 0; index < len(arguments); index += 2 {
		path, script := arguments[index], arguments[index+1]
		if path == "" {
			return nil, fmt.Errorf("path must not be empty")
		}
		total += len(script)
		if len(script) > maxMekugiScriptBytes || total > maxMekugiScriptBytes {
			return nil, fmt.Errorf("script exceeds %d bytes", maxMekugiScriptBytes)
		}
		edits = append(edits, mekugi.FileEdit{Path: path, Script: script})
	}
	return edits, nil
}

func parseHpatchRecoveryArguments(handler *interp.HandlerContext, arguments []string) (payload string, scriptIndex int, err error) {
	var positional []string
	for index := 0; index < len(arguments); index++ {
		switch arguments[index] {
		case "--script":
			if index+1 >= len(arguments) || arguments[index+1] == "" {
				return "", 0, fmt.Errorf("hpatch --recover --script requires N")
			}
			if scriptIndex != 0 {
				return "", 0, fmt.Errorf("duplicate --script selector")
			}
			scriptIndex, err = strconv.Atoi(arguments[index+1])
			if err != nil || scriptIndex < 1 {
				return "", 0, fmt.Errorf("hpatch --recover --script requires a positive integer")
			}
			index++
		default:
			positional = append(positional, arguments[index])
		}
	}
	if len(positional) > 1 {
		return "", 0, fmt.Errorf("usage: hpatch --recover HANDLE [SCRIPT] [--script N]")
	}
	if len(positional) == 1 {
		if len(positional[0]) > maxMekugiScriptBytes {
			return "", 0, fmt.Errorf("script exceeds %d bytes", maxMekugiScriptBytes)
		}
		return positional[0], scriptIndex, nil
	}
	payload, err = readHpatchStdin(handler)
	return payload, scriptIndex, err
}
