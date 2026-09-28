package terminal

import (
	"errors"
	"testing"
)

func TestFaintOverride(t *testing.T) {
	for _, tc := range []struct {
		mode    string
		want    bool
		invalid bool
	}{{"on", true, false}, {"off", false, false}, {"yes", false, true}} {
		got, err := SupportsFaint(t.Context(), tc.mode)
		if got != tc.want || (err != nil) != tc.invalid {
			t.Fatalf("%s = %v, %v", tc.mode, got, err)
		}
	}
}

func TestMoshAncestry(t *testing.T) {
	for _, tc := range []struct {
		name  string
		nodes map[int]struct {
			pid  int
			name string
		}
		want bool
	}{
		{"direct", map[int]struct {
			pid  int
			name string
		}{3: {2, "mekugi"}, 2: {1, "mosh-server"}}, true},
		{"nested", map[int]struct {
			pid  int
			name string
		}{3: {2, "mekugi"}, 2: {4, "fish"}, 4: {1, "/usr/bin/mosh-server"}}, true},
		{"ssh", map[int]struct {
			pid  int
			name string
		}{3: {2, "mekugi"}, 2: {1, "sshd"}}, false},
		{"detached", map[int]struct {
			pid  int
			name string
		}{3: {2, "mekugi"}, 2: {1, "herdr"}, 9: {1, "mosh-server"}}, false},
		{"cycle", map[int]struct {
			pid  int
			name string
		}{3: {2, "mekugi"}, 2: {3, "shell"}}, false},
		{"missing", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := moshAncestor(3, func(pid int) (int, string, error) {
				n, ok := tc.nodes[pid]
				if !ok {
					return 0, "", errors.New("missing")
				}
				return n.pid, n.name, nil
			})
			if got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
