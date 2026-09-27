package main

import (
	"fmt"
	"strconv"
	"strings"
)

// The native client deliberately rejects unmapped TUI flags. Passing them
// through to a different subcommand would silently change their meaning.
func appServerArgs(args []string) ([]string, string, error) {
	out := []string{"app-server"}
	yolo := false
	resume := ""
	resumeCommand := false
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; arg {
		case "resume":
			if resumeCommand {
				return nil, "", fmt.Errorf("native UI accepts only one resume command")
			}
			resumeCommand = true
		case "--last":
			if !resumeCommand || resume != "" {
				return nil, "", fmt.Errorf("use resume --last without a thread ID")
			}
			resume = "--last"
		case "--yolo", "--dangerously-bypass-approvals-and-sandbox":
			yolo = true
		case "-c", "--config", "-m", "--model":
			if i+1 == len(args) {
				return nil, "", fmt.Errorf("%s requires a value", arg)
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
					return nil, "", fmt.Errorf("%s requires a value", flag)
				}
				out = append(out, "-c", "model="+strconv.Quote(model))
			} else if resumeCommand && resume == "" && !strings.HasPrefix(arg, "-") && strings.TrimSpace(arg) != "" {
				resume = arg
			} else {
				return nil, "", fmt.Errorf("native UI does not yet support %q; use --yolo, -m, -c, and resume THREAD_ID or resume --last, and enter prompts in Main", arg)
			}
		}
	}
	if resumeCommand && resume == "" {
		return nil, "", fmt.Errorf("native UI resume requires a thread ID or --last; picker is not supported yet")
	}
	if !yolo {
		return nil, "", fmt.Errorf("native UI currently requires explicit --yolo")
	}
	return append(out, "-c", `approval_policy="never"`, "-c", `sandbox_mode="danger-full-access"`), resume, nil
}
