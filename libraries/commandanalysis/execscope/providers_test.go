package execscope

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestFormatterLiteralBraceTargets(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"first.go", "second.go", "third.go", "1.go", "2.go", "{first,second}.go"} {
		writeTestFile(t, filepath.Join(root, name), "package p\n")
	}
	for _, test := range []struct {
		command, shell string
		want           []string
	}{
		{"gofmt -w {first,second}.go", "bash", []string{"first.go", "second.go"}},
		{"gofmt -w {{first,second},third}.go", "bash", []string{"first.go", "second.go", "third.go"}},
		{"gofmt -w {1..2}.go", "bash", []string{"1.go", "2.go"}},
		{"gofmt -w '{first,second}.go'", "bash", []string{"{first,second}.go"}},
		{"gofmt -w {first,second}.go", "sh", nil},
		{"set +B; gofmt -w {first,second}.go", "bash", nil},
		{"set +o braceexpand; gofmt -w {first,second}.go", "bash", nil},
		{"set $OPTIONS; gofmt -w {first,second}.go", "bash", nil},
		{"gofmt -w {$TARGET,second}.go", "bash", nil},
		{"gofmt -w {$(touch marker),second}.go", "bash", nil},
		{"gofmt -w {*.go,second.go}", "bash", nil},
		{"gofmt -w {1..4097}.go", "bash", nil},
	} {
		t.Run(test.shell+"/"+test.command, func(t *testing.T) {
			plan := Classify(Input{Command: test.command, Directory: root, Shell: test.shell, AuthoredOnly: true})
			var paths []string
			for _, entry := range plan.Scope {
				for _, operand := range entry.Operands {
					paths = append(paths, filepath.Base(operand.Path))
				}
			}
			if !slices.Equal(paths, test.want) || len(test.want) == 0 && plan.Class != Opaque {
				t.Fatalf("scope = %q, class=%v, want %q; %s", paths, plan.Class, test.want, plan.Reason)
			}
		})
	}
}

func TestExecProviderDeadlineAndDenoPermissionSet(t *testing.T) {
	root := t.TempDir()
	plan := classifyExecShellSource(`python3 -c 'open("a","w")'`, root, "bash", time.Now().Add(-time.Second), 0, nil, false)
	if plan.Class != Opaque || !strings.Contains(plan.Reason, "deadline") {
		t.Fatalf("expired provider: %+v", plan)
	}
	writeTestFile(t, filepath.Join(root, "deno.json"), `{"permissions":{"default":{"write":["src"]},"spawn":{"write":["src"],"run":true}}}`)
	paths, open := execDenoPermissionSet(root, "default")
	if open || len(paths) != 1 || paths[0] != filepath.Join(root, "src") {
		t.Fatalf("permission set: %v %v", paths, open)
	}
	if _, open := execDenoPermissionSet(root, "spawn"); !open {
		t.Fatal("subprocess permissions closed the scope")
	}
}
