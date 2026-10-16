package router

import (
	"path/filepath"

	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func debugStorageDirectory() (string, error) {
	base, err := history.StateDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "debug"), nil
}
