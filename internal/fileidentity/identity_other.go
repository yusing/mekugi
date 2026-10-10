//go:build !unix

package fileidentity

import "os"

func FromInfo(os.FileInfo) string { return "" }
