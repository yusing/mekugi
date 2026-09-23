package runtimepath

import (
	"strings"
	"testing"
)

func TestDirectoryRequiresAbsoluteConfiguredPath(t *testing.T) {
	t.Setenv(DirectoryEnvironment, "relative")
	if _, err := Directory(); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("Directory() error = %v", err)
	}
}
