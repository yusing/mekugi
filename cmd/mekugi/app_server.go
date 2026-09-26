package main

import (
	"fmt"
	"strconv"
	"strings"
)

// The feasibility client deliberately rejects unmapped TUI flags. Passing them
// through to a different subcommand would silently change their meaning.
func appServerArgs(args []string) ([]string, error) {
	out := []string{"app-server"}
	yolo := false
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; arg {
		case "--yolo", "--dangerously-bypass-approvals-and-sandbox":
			yolo = true
		case "-c", "--config", "-m", "--model":
			if i+1 == len(args) {
				return nil, fmt.Errorf("%s requires a value", arg)
			}
			i++
			value := args[i]
			if arg == "-m" || arg == "--model" {
				value = "model=" + strconv.Quote(value)
			}
			out = append(out, "-c", value)
		default:
			if after, ok := strings.CutPrefix(arg, "--config="); ok {
				out = append(out, "-c", after)
			} else if flag, model, ok := strings.Cut(arg, "="); ok && (flag == "-m" || flag == "--model") {
				if model == "" {
					return nil, fmt.Errorf("%s requires a value", flag)
				}
				out = append(out, "-c", "model="+strconv.Quote(model))
			} else {
				return nil, fmt.Errorf("app-server preview does not yet support %q; use --yolo, -m, and -c only, and enter prompts in Main", arg)
			}
		}
	}
	if !yolo {
		return nil, fmt.Errorf("app-server preview currently requires explicit --yolo")
	}
	return append(out, "-c", `approval_policy="never"`, "-c", `sandbox_mode="danger-full-access"`), nil
}
