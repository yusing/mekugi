package persistence

import "testing"

func TestOpenRetainedRejectsWindowsDevice(t *testing.T) {
	file, err := OpenRetained("NUL", 1024)
	if file != nil {
		file.Close()
	}
	if err == nil {
		t.Fatal("accepted device as a retained record")
	}
}
