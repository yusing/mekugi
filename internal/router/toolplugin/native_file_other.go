//go:build !unix

package toolplugin

import "os"

func openNativeSource(path string) (*os.File, error) { return os.Open(path) }
