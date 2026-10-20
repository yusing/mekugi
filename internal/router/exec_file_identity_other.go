//go:build !unix

package router

import "os"

func execFileIdentity(os.FileInfo) string {
	return ""
}
