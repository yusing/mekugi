package router

import "path/filepath"

func debugStorageDirectory() (string, error) {
	base, err := mekugiStateDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "debug"), nil
}
