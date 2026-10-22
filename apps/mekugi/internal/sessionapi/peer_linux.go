package sessionapi

import (
	"net"

	"golang.org/x/sys/unix"
)

// peer reports the user at the other end of a Unix socket connection,
// as the kernel recorded it.
func peer(conn net.Conn) (uid int, err error) {
	var cred *unix.Ucred
	err = control(conn, func(fd int) (err error) {
		cred, err = unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
		return err
	})
	if err != nil {
		return 0, err
	}
	return int(cred.Uid), nil
}
