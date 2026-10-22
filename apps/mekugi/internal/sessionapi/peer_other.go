//go:build !linux && !darwin

package sessionapi

import (
	"errors"
	"net"
)

// peer cannot identify a connection's other end here, so no session connects.
func peer(net.Conn) (uid int, err error) {
	return 0, errors.New("terminal sessions need peer credentials, which this system does not provide")
}
