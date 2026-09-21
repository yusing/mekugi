//go:build unix

package router

import "os"

func processHasControllingTerminal() bool {
	terminal, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	_ = terminal.Close()
	return true
}
