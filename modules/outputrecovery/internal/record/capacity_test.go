package record

import (
	"strconv"
	"strings"
	"testing"
)

func TestReadCapacityErrorIncludesLimitAndRemedy(t *testing.T) {
	err := Validate(Record{Stdout: strings.Repeat("x", MaxOutputBytes+1)})
	if err == nil || !strings.Contains(err.Error(), "limit is "+strconv.Itoa(MaxOutputBytes)+" bytes") || !strings.Contains(err.Error(), "split the operation") {
		t.Fatalf("missing capacity diagnostic: %v", err)
	}
}
