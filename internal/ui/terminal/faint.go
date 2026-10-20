package terminal

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// SupportsFaint applies an invocation-local override or checks this process's
// ancestors for mosh-server. Detached multiplexers may hide that ancestry;
// their callers can explicitly choose off. Unrelated sessions are never scanned.
func SupportsFaint(ctx context.Context, mode string) (bool, error) {
	switch mode {
	case "on":
		return true, nil
	case "off":
		return false, nil
	case "auto":
	default:
		return false, fmt.Errorf("--ansi-faint must be auto, on, or off")
	}
	ctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	return !moshAncestor(os.Getpid(), func(pid int) (int, string, error) { return parentProcess(ctx, pid) }), nil
}

func moshAncestor(pid int, parent func(int) (int, string, error)) bool {
	seen := make(map[int]bool)
	for range 64 {
		if pid <= 1 || seen[pid] {
			break
		}
		seen[pid] = true
		next, name, err := parent(pid)
		if err != nil {
			break
		}
		if filepath.Base(name) == "mosh-server" {
			return true
		}
		pid = next
	}
	return false
}

func parentProcess(ctx context.Context, pid int) (int, string, error) {
	if runtime.GOOS == "linux" {
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			return 0, "", err
		}
		before, after, ok := strings.CutLast(string(data), ")")
		_, name, named := strings.Cut(before, "(")
		fields := strings.Fields(after)
		if !ok || !named || len(fields) < 2 {
			return 0, "", fmt.Errorf("invalid process stat")
		}
		parent, err := strconv.Atoi(fields[1])
		return parent, name, err
	}
	data, err := exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "ppid=", "-o", "comm=").Output()
	if err != nil {
		return 0, "", err
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return 0, "", fmt.Errorf("invalid process metadata")
	}
	parent, err := strconv.Atoi(fields[0])
	return parent, strings.Join(fields[1:], " "), err
}
