package router

import (
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
)

// nextCaptureOrder reserves a store-wide display sequence under the replay lock.
// It is persisted before the capture, so retries can leave gaps but cannot move
// an existing capture. Receipts and viewer refreshes do not change this order.
func (s *mekugiReplayStore) nextCaptureOrder() (uint64, error) {
	const name = "capture-order"
	path := filepath.Join(s.directory, name)
	var previous uint64
	info, err := os.Lstat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	if err == nil {
		if !info.Mode().IsRegular() || info.Size() != 8 {
			return 0, errors.New("invalid capture order counter")
		}
		file, err := os.Open(path)
		if err != nil {
			return 0, err
		}
		data, err := io.ReadAll(io.LimitReader(file, 9))
		err = errors.Join(err, file.Close())
		if err != nil {
			return 0, err
		}
		if len(data) != 8 {
			return 0, errors.New("invalid capture order counter")
		}
		previous = binary.BigEndian.Uint64(data)
	}
	if previous == math.MaxUint64 {
		return 0, errors.New("capture order capacity reached")
	}
	next := previous + 1
	var data [8]byte
	binary.BigEndian.PutUint64(data[:], next)
	return next, s.writeFile(name, "capture-order-pending-", data[:])
}
