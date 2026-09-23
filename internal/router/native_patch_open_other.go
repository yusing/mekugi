//go:build !unix

package router

import "os"

func openNativePatchFile(path string) (*os.File, error) {
	return os.Open(path)
}

func execFileIdentity(os.FileInfo) string {
	return ""
}
