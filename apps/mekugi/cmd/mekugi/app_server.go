package main

import (
	"fmt"
	"strconv"
	"strings"
)

// Preserve supplied options after native validation, without the old resume
// command or selector. The exit renderer appends the actual current thread.
func appServerResumeArgv(executable string, routerArgs, args []string) []string {
	argv := append([]string{executable}, routerArgs...)
	if len(routerArgs) > 0 && routerArgs[len(routerArgs)-1] == "third-party" {
		argv = argv[:len(argv)-1]
	} else if len(routerArgs) == 0 || routerArgs[len(routerArgs)-1] != "grok" {
		argv = append(argv, "codex")
	}
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; arg {
		case "resume", "--last":
		case "-c", "--config", "-m", "--model", "--enable", "--disable":
			argv = append(argv, arg, args[i+1])
			i++
		default:
			if strings.HasPrefix(arg, "-") {
				argv = append(argv, arg)
			}
		}
	}
	return argv
}

// The native client deliberately rejects unmapped TUI flags. Passing them
// through to a different subcommand would silently change their meaning.
// Without --yolo, Codex's configured approval and sandbox policy applies and
// the UI answers its approval requests.
func appServerArgs(args []string) ([]string, string, bool, error) {
	out := []string{"app-server"}
	yolo := false
	resume := ""
	resumeCommand := false
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; arg {
		case "resume":
			if resumeCommand {
				return nil, "", false, fmt.Errorf("UI accepts only one resume command")
			}
			resumeCommand = true
		case "--last":
			if !resumeCommand || resume != "" {
				return nil, "", false, fmt.Errorf("use resume --last without a thread ID")
			}
			resume = "--last"
		case "--yolo", "--dangerously-bypass-approvals-and-sandbox":
			yolo = true
		case "--enable", "--disable":
			if i+1 == len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "-") {
				return nil, "", false, fmt.Errorf("%s requires a feature", arg)
			}
			i++
			// Keep host validation and toggle precedence, rather than translating
			// these into config overrides. Codex owns instant-interrupt execution.
			// Source: codex-rs/cli/src/main.rs:933:978@68e1a421 FeatureToggles
			out = append(out, arg, args[i])
		case "-c", "--config", "-m", "--model":
			if i+1 == len(args) {
				return nil, "", false, fmt.Errorf("%s requires a value", arg)
			}
			i++
			value := args[i]
			if arg == "-m" || arg == "--model" {
				value = "model=" + strconv.Quote(value)
			}
			out = append(out, "-c", value)
		default:
			if flag, feature, ok := strings.Cut(arg, "="); ok && (flag == "--enable" || flag == "--disable") {
				if feature == "" {
					return nil, "", false, fmt.Errorf("%s requires a feature", flag)
				}
				out = append(out, arg)
			} else if after, ok := strings.CutPrefix(arg, "--config="); ok {
				out = append(out, "-c", after)
			} else if flag, model, ok := strings.Cut(arg, "="); ok && (flag == "-m" || flag == "--model") {
				if model == "" {
					return nil, "", false, fmt.Errorf("%s requires a value", flag)
				}
				out = append(out, "-c", "model="+strconv.Quote(model))
			} else if resumeCommand && resume == "" && !strings.HasPrefix(arg, "-") && strings.TrimSpace(arg) != "" {
				resume = arg
			} else {
				return nil, "", false, fmt.Errorf("UI does not yet support %q; use --yolo, -m, -c, --enable, --disable, and resume [THREAD_ID | --last], and enter prompts in Main", arg)
			}
		}
	}
	if resumeCommand && resume == "" {
		resume = "--pick" // The UI opens its session picker.
	}
	if !yolo {
		return out, resume, false, nil
	}
	return append(out, "-c", `approval_policy="never"`, "-c", `sandbox_mode="danger-full-access"`), resume, true, nil
}
