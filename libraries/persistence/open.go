package persistence

import (
	"errors"
	"os"
)

// OpenRetained validates the opened record, not its pre-open inode: publication
// may atomically replace a regular record between selection and opening. Platform
// openers reject symlinks or validate the descriptor against regular pathname
// observations before admitting contents. Unix opens nonblocking; Windows uses
// overlapped reparse-point handles. The WASI and portable fallback openers expose
// no open-time nonblocking flag here, so a concurrent special-file replacement
// may block opening on those targets before descriptor validation can run.
// Source: libraries/legacycall/open.go:8:24@[e448711c] openRetained
func OpenRetained(path string, limit int) (*os.File, error) {
	file, err := openRetainedFile(path)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err == nil && (!info.Mode().IsRegular() || info.Size() > int64(limit)) {
		err = errors.New("invalid retained file")
	}
	if err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}
