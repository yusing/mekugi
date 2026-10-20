package router

import (
	"strconv"
	"strings"
	"testing"
)

func TestReadCapacityErrorIncludesLimitAndRemedy(t *testing.T) {
	err := validateReadRecord(shellOutputRecord{Stdout: strings.Repeat("x", maxShellOutputBytes+1)})
	if err == nil || !strings.Contains(err.Error(), "limit is "+strconv.Itoa(maxShellOutputBytes)+" bytes") || !strings.Contains(err.Error(), "split the operation") {
		t.Fatalf("missing capacity diagnostic: %v", err)
	}
}
