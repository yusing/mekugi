// Source: apps/mekugi/internal/router/worker_executable_linux.go:1:8@[7b6d9ccc]
package frontends

import "os"

// The installation pathname may already name a replacement binary, or be gone.
func openRunningExecutable() (*os.File, error) {
	return os.Open("/proc/self/exe")
}
