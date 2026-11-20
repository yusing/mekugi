package query

import (
	"bytes"
	"crypto/rand"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// darwin has no child subreaper, and orphans re-parent to launchd, so parentage
// cannot identify resolver descendants after their parent exits. The dedicated
// worker instead marks its environment, which resolvers and their descendants
// inherit, and cleanup retires same-user processes that carry the mark.
const frontendCleanupEnvironment = "MEKUGI_SYMBOL_RESOLVER_OWNER"

var frontendCleanupMark []byte

func prepareFrontendCleanup() error {
	token := rand.Text()
	if err := os.Setenv(frontendCleanupEnvironment, token); err != nil {
		return err
	}
	frontendCleanupMark = []byte("\x00" + frontendCleanupEnvironment + "=" + token + "\x00")
	return nil
}

func cleanupFrontendOrphans() {
	if frontendCleanupMark == nil {
		return
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		processes, err := unix.SysctlKinfoProcSlice("kern.proc.uid", os.Getuid())
		if err != nil {
			return
		}
		found := false
		for i := range processes {
			pid := int(processes[i].Proc.P_pid)
			if pid <= 0 || pid == os.Getpid() {
				continue
			}
			// kern.procargs2 holds argc, the executable path, argv, then the
			// initial environment, each NUL-terminated. Zombies have none.
			arguments, err := unix.SysctlRaw("kern.procargs2", pid)
			if err != nil || !bytes.Contains(arguments, frontendCleanupMark) {
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
