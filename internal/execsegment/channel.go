package execsegment

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// ReportDirectory validates a request ID before resolving its private files.
func ReportDirectory(directory, id string) (string, bool) {
	decoded, err := hex.DecodeString(id)
	if err != nil || len(decoded) != 16 {
		return "", false
	}
	return filepath.Join(directory, "run-"+id), true
}

// OpenReport sends an atomic request through an existing FIFO, followed by
// router-created FIFOs.
// The sandboxed helper only opens files; it creates no filesystem objects.
// Report has one writer, so output messages need no shared-pipe framing.
func OpenReport(channel, directory string, timeout time.Duration) (report, reply *os.File, work string, err error) {
	deadline := time.Now().Add(timeout)
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return
	}
	id := hex.EncodeToString(random[:])
	work, _ = ReportDirectory(directory, id)
	if err = RequestReport(channel, id); err != nil {
		return nil, nil, "", err
	}
	return OpenPreparedReport(work, time.Until(deadline))
}

// RequestReport asks the router to create one invocation's existing resources.
func RequestReport(channel, id string) error {
	fd, err := unix.Open(channel, unix.O_WRONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	// The 33-byte line is below POSIX PIPE_BUF. A full or absent reader
	// declines tracking instead of blocking the command's startup.
	n, err := unix.Write(fd, []byte(id+"\n"))
	unix.Close(fd)
	if err != nil || n != len(id)+1 {
		return errors.Join(err, errors.New("tracking request not sent"))
	}
	return nil
}

// OpenPreparedReport opens router-created resources requested earlier by dash.
func OpenPreparedReport(work string, timeout time.Duration) (report, reply *os.File, directory string, err error) {
	deadline := time.Now().Add(timeout)
	var fd int
	for {
		fd, err = unix.Open(filepath.Join(work, "report"), unix.O_WRONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err == nil {
			break
		}
		if (!errors.Is(err, unix.ENOENT) && !errors.Is(err, unix.ENXIO)) || time.Now().After(deadline) {
			return nil, nil, "", err
		}
		time.Sleep(time.Millisecond)
	}
	report = os.NewFile(uintptr(fd), filepath.Join(work, "report"))
	fd, err = unix.Open(filepath.Join(work, "reply"), unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		report.Close()
		return nil, nil, "", err
	}
	reply = os.NewFile(uintptr(fd), filepath.Join(work, "reply"))
	return report, reply, work, nil
}
