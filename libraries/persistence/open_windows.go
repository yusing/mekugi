package persistence

import (
	"os"
	"syscall"
)

func openRetainedFile(path string) (*os.File, error) {
	// Open the reparse point itself, so descriptor validation rejects links.
	// Go preserves these CreateFile flags and configures overlapped handles.
	return os.OpenFile(path, os.O_RDONLY|syscall.FILE_FLAG_OPEN_REPARSE_POINT|syscall.FILE_FLAG_OVERLAPPED, 0)
}
