package router

import (
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/yusing/mekugi/internal/livediff"
)

// Source: codex-rs/tui/src/token_usage.rs:20:88@1cc7e236 TokenUsage
// Source: codex-rs/tui/src/app/exit_summary.rs:132:142@1cc7e236 format_exit_messages
func (u *appServerUI) writeExitSummary(w io.Writer, color bool) error {
	var text strings.Builder
	usage := u.exitUsage
	if usage.TotalTokens != 0 {
		input := usage.InputTokens - min(usage.InputTokens, usage.CachedInputTokens)
		fmt.Fprintf(&text, "Token usage: total=%s input=%s", exitTokenCount(input+usage.OutputTokens), exitTokenCount(input))
		if usage.CachedInputTokens > 0 {
			fmt.Fprintf(&text, " (+ %s cached)", exitTokenCount(usage.CachedInputTokens))
		}
		fmt.Fprintf(&text, " output=%s", exitTokenCount(usage.OutputTokens))
		if usage.ReasoningOutputTokens > 0 {
			fmt.Fprintf(&text, " (reasoning %s)", exitTokenCount(usage.ReasoningOutputTokens))
		}
		text.WriteByte('\n')
	}
	commandLine := func(argv []string) string {
		command := livediff.Safe(workerCommand(argv[0], argv[1:]), false)
		if color {
			command = "\x1b[36m" + command + "\x1b[39m"
		}
		return command
	}
	if u.thread != "" {
		argv := slices.Clone(u.resumeArgv)
		if len(argv) == 0 {
			argv = []string{"mekugi", "codex", "--yolo"}
		}
		command := commandLine(append(argv, "resume", u.thread))
		fmt.Fprintf(&text, "To continue this session, run:\n  %s\n", command)
		replay := []string{argv[0], "replay-session", "--session", u.thread}
		if u.replayDebugDirectory != "" {
			replay = append(replay, "--debug-dir", u.replayDebugDirectory)
		}
		fmt.Fprintf(&text, "To replay this session offline, run:\n  %s\n", commandLine(replay))
	}
	if text.Len() == 0 {
		return nil
	}
	_, err := io.WriteString(w, text.String())
	return err
}

func exitTokenCount(n uint64) string {
	s := strconv.FormatUint(n, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
