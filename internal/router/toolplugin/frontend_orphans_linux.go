package toolplugin

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func enableFrontendSubreaper() error {
	// This dedicated worker adopts resolver descendants after their immediate
	// parent exits. The worker itself remains in Codex's command group.
	_, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, 36 /* PR_SET_CHILD_SUBREAPER */, 1, 0, 0, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func cleanupFrontendOrphans() {
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		entries, err := os.ReadDir("/proc")
		if err != nil {
			return
		}
		found := false
		for _, entry := range entries {
			pid, err := strconv.Atoi(entry.Name())
			if err != nil || pid <= 0 || !frontendChildOf(pid, os.Getpid()) {
				continue
			}
			found = true
			_ = syscall.Kill(pid, syscall.SIGKILL)
			var status syscall.WaitStatus
			_, _ = syscall.Wait4(pid, &status, syscall.WNOHANG, nil)
		}
		if !found {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func frontendChildOf(pid, parent int) bool {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return false
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if value, ok := strings.CutPrefix(line, "PPid:"); ok {
			found, err := strconv.Atoi(strings.TrimSpace(value))
			return err == nil && found == parent
		}
	}
	return false
}
