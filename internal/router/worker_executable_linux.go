package router

import "os"

// The installation pathname may already name a replacement binary, or be gone.
func openRunningExecutable() (*os.File, error) {
	return os.Open("/proc/self/exe")
}
