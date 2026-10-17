package executionobservation

import (
	"fmt"

	"os"

	"path/filepath"

	"testing"
)

func storageInspectionFiles(t *testing.T, directory string) map[string]string {
	t.Helper()
	files := make(map[string]string)
	err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		var data []byte
		if !entry.IsDir() {
			data, err = os.ReadFile(path)
			if err != nil {
				return err
			}
		}
		files[path] = fmt.Sprintf("%s %s %x", info.Mode(), info.ModTime(), data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}
