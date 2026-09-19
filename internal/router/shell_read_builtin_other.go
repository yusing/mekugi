//go:build !unix

package router

import (
	"fmt"
	"os"
)

func shellReadOwnedFile(*os.File) (*os.File, error) {
	return nil, fmt.Errorf("enhanced read is unsupported on this platform")
}

func shellReadReady(*os.File) (bool, error) {
	return false, fmt.Errorf("read -t 0 is unsupported on this platform")
}
