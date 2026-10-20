package capture

import (
	"path/filepath"
	"testing"
	"time"
)

func TestExecCaptureNamesEverySharedOrigin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.go")
	writeTestFile(t, path, "package a\n")
	capture := newExecCapture(time.Now().Add(time.Minute))
	for _, origin := range []string{"", "gofmt", "goimports", "gofmt"} {
		capture.origin = origin
		capture.add(path)
	}
	if len(capture.files) != 1 || capture.files[0].Origin != "" || capture.files[0].AlsoManaged != "gofmt, goimports" {
		t.Fatalf("shared capture = %+v, want one direct file also managed by gofmt, goimports", capture.files)
	}
}
