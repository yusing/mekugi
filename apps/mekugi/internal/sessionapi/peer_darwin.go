package sessionapi

import (
	"net"

	"golang.org/x/sys/unix"
)

// peer reports the user at the other end of a Unix socket connection,
// as the kernel recorded it.
func peer(conn net.Conn) (uid int, err error) {
	err = control(conn, func(fd int) error {
		cred, err := unix.GetsockoptXucred(fd, unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if err != nil {
			return err
		}
		uid = int(cred.Uid)
		return nil
	})
	return uid, err
}
