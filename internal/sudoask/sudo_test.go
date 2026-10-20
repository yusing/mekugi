package sudoask

import "testing"

func TestSudoInteractive(t *testing.T) {
	for _, tc := range []struct {
		args                  []string
		interactive, approval bool
	}{
		{[]string{"id"}, true, true}, {[]string{"-u", "root", "id", "-l"}, true, true},
		{[]string{"-uroot", "id"}, true, true}, {[]string{"-v"}, true, true}, {[]string{"--no-update", "id"}, true, true}, {[]string{"-k", "id"}, true, true},
		{[]string{"-l"}, false, false}, {[]string{"-u", "root", "-ll", "id"}, false, false},
		{[]string{"--list", "id"}, false, false}, {[]string{"-n", "id"}, false, true},
		{[]string{"-S", "id"}, false, true}, {[]string{"-A", "id"}, false, true},
		{[]string{"--askpass", "id"}, false, true}, {[]string{"-K"}, false, false},
		{[]string{"FOO=x", "-S", "id"}, false, true}, {[]string{"--std", "id"}, false, true},
		{[]string{"FOO=x", "--us", "root", "id"}, true, true},
		{[]string{"-k"}, false, false}, {[]string{"-h"}, false, false}, {[]string{"--version"}, false, false},
	} {
		if got := CommandApproval(tc.args); got != tc.approval {
			t.Errorf("%q: command approval got %v want %v", tc.args, got, tc.approval)
		}
		if got := Interactive(tc.args); got != tc.interactive {
			t.Errorf("%q: got %v want %v", tc.args, got, tc.interactive)
		}
	}
}
