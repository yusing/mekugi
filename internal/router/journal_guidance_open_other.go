//go:build !unix

package router

import (
	"errors"
	"os"
)

func openJournalGuidanceFile(string) (*os.File, error) {
	return nil, errors.New("link-safe guidance snapshots are unavailable on this platform")
}
