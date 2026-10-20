package execscope

import (
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestClassifyRetainedChangeResolution(t *testing.T) {
	directory := t.TempDir()
	deadline := time.Now().Add(Budget)
	target := filepath.Join(directory, "selected.txt")
	for _, test := range []struct {
		name     string
		resolved ChangeResolution
		class    Class
		reason   string
	}{
		{"retained paths", ChangeResolution{Paths: []string{target}}, Declared, ""},
		{"invalid arguments", ChangeResolution{InvalidArguments: true}, Opaque, "mchanges apply arguments are not valid"},
		{"unavailable history", ChangeResolution{}, Opaque, "mchanges apply history is unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			called := false
			result := Classify(Input{Command: "mchanges apply amber1 -- selected.txt", Directory: directory, Shell: "bash", Deadline: deadline,
				Changes: func(args []string, cwd string, until time.Time) ChangeResolution {
					called = true
					if !slices.Equal(args, []string{"apply", "amber1", "--", "selected.txt"}) || cwd != directory || !until.Equal(deadline) {
						t.Fatalf("resolver metadata changed: %q %q %v", args, cwd, until)
					}
					return test.resolved
				}})
			if !called || result.Class != test.class || result.Reason != test.reason {
				t.Fatalf("resolved classification: called=%v result=%+v", called, result)
			}
			if test.class == Declared && (len(result.Scope) != 1 || result.Scope[0].Operands[0].Path != target) {
				t.Fatalf("retained target lost: %+v", result)
			}
		})
	}
}
