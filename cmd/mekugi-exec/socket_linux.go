package main

import "golang.org/x/sys/unix"

func newUnixSocket() (int, error) {
	return unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
}
