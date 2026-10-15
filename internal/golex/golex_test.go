package golex

import "testing"

func TestIdentifier(t *testing.T) {
	for _, value := range []string{"alpha", "_alpha", "世界9"} {
		if !IsIdentifier(value) {
			t.Errorf("IsIdentifier(%q) = false", value)
		}
	}
	for _, value := range []string{"", "9alpha", "func", "a-b"} {
		if IsIdentifier(value) {
			t.Errorf("IsIdentifier(%q) = true", value)
		}
	}
}
