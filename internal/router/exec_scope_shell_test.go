package router

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func classifyExecShell(command, workdir, shell string) execPlan {
	return classifyExecShellWithin(command, workdir, shell, time.Now().Add(execProviderBudget), 0)
}

func execPlanScope(plan execPlan, workdir string) []string {
	var scope []string
	for _, entry := range plan.Scope {
		var operands []string
		for _, operand := range entry.Operands {
			path := strings.TrimPrefix(operand.Path, workdir+"/")
			if operand.Glob {
				path += "(glob)"
			}
			operands = append(operands, path)
		}
		text := string(entry.Kind) + ":" + strings.Join(operands, ",")
		if entry.Kind == execScopeInto {
			text += "->" + strings.TrimPrefix(entry.Dest, workdir+"/")
			if entry.DestDir {
				text += "/"
			}
		}
		if entry.Backup != "" {
			text += "+backup" + entry.Backup
		}
		scope = append(scope, text)
	}
	return scope
}

func TestClassifyExecShellDeclaredOperations(t *testing.T) {
	const workdir = "/work"
	for _, test := range []struct {
		command string
		labels  []string
		scope   []string
	}{
		{"cp a.txt b.txt", []string{"cp"}, []string{"into:a.txt->b.txt"}},
		{"cp -r src dst", []string{"cp"}, []string{"into:src->dst"}},
		{"cp a b dir", []string{"cp"}, []string{"into:a,b->dir/"}},
		{"cp -t dir a", []string{"cp"}, []string{"into:a->dir/"}},
		{"cp --backup=simple a b", []string{"cp"}, []string{"into:a->b+backup~"}},
		{"ln -S .orig --backup=never a b", []string{"ln"}, []string{"into:a->b+backup.orig"}},
		{"mv old.go new.go", []string{"mv"}, []string{"into:old.go->new.go"}},
		{"rm -f *.tmp", []string{"rm"}, []string{"file:*.tmp(glob)"}},
		{"rm -rf build", []string{"rm"}, []string{"tree:build"}},
		{"touch x && tee y < /dev/null", []string{"touch", "tee"}, []string{"file:x", "file:y"}},
		{"cd sub && rm z", []string{"rm"}, []string{"file:sub/z"}},
		{"printf 'x\\n' > out.txt", []string{"printf"}, []string{"file:out.txt"}},
		{"cat <<'EOF' >> log.txt\nline\nEOF", []string{"cat"}, []string{"file:log.txt"}},
		{"sed -i 's/a/b/' f.go", []string{"sed"}, []string{"file:f.go"}},
		{"ln -s target link", []string{"ln"}, []string{"into:target->link"}},
		{"git mv a b", []string{"git mv"}, nil},
		{"echo hi > /dev/null", nil, nil},
		{"cd sub || exit 1\nrm z", []string{"rm"}, []string{"file:sub/z"}},
		{"cd sub && cd deeper && rm z", []string{"rm"}, []string{"file:sub/deeper/z"}},
		{"(cd sub && rm z); rm y", []string{"rm"}, []string{"file:sub/z", "file:y"}},
		{"cd sub; rm /abs/z", []string{"rm"}, []string{"file:/abs/z"}},
		{"rm [!a]*.log", []string{"rm"}, []string{"file:[^a]*.log(glob)"}},
		{"sort -uo out.txt in.txt", []string{"sort"}, []string{"file:out.txt"}},
		{"sort --output out.txt in.txt", []string{"sort"}, []string{"file:out.txt"}},
		{"sort -k2 -t, in.txt", nil, nil},
		{"sed -n ':a;N;$!ba;p' f", nil, nil},
		{"awk -F, '{print $1}' \"$f\"", nil, nil},
		{"/usr/bin/rm a", []string{"rm"}, []string{"file:a"}},
		{"cat \"$f\" > out", []string{"cat"}, []string{"file:out"}},
	} {
		plan := classifyExecShell(test.command, workdir, "bash")
		if plan.Class != execDeclared && !(test.labels == nil && plan.Class == execNeutral) {
			t.Errorf("%q class = %v (%s)", test.command, plan.Class, plan.Reason)
			continue
		}
		if !slices.Equal(plan.Labels, test.labels) {
			t.Errorf("%q labels = %q, want %q", test.command, plan.Labels, test.labels)
		}
		if scope := execPlanScope(plan, workdir); test.scope != nil && !slices.Equal(scope, test.scope) {
			t.Errorf("%q scope = %q, want %q", test.command, scope, test.scope)
		}
	}
}

func TestClassifyExecShellNeutralAndOpaque(t *testing.T) {
	const workdir = "/work"
	for _, command := range []string{
		"ls -la", "git status && git diff", "rg foo | head", "go version", "cat a.txt",
		"if test -f a; then echo yes; fi", "git config --get user.name", "fd -e go", "less -R a",
	} {
		if plan := classifyExecShell(command, workdir, "bash"); plan.Class != execNeutral {
			t.Errorf("%q class = %v (%s), want neutral", command, plan.Class, plan.Reason)
		}
	}
	for _, command := range []string{
		"python3 script.py", "make build", "cp --parents a/b dst", "rm $FILE", "cp ~/a b",
		"sleep 10 &", "git commit -m x", "find . -delete", "awk '{print > \"x\"}' f",
		"sed 'w out' f", "eval rm a", "git stash",
		// The directory is unknown after a cd that may have failed.
		"cd sub; rm z", "cd sub && rm a; rm z", "cd sub || rm a\nrm z", "cd -; rm z",
		"if true; then cd sub; fi; rm z",
		// Substitutions run commands even where no operand uses them.
		"cat <<EOF > out\n$(rm x)\nEOF", "[[ $(rm x) ]]", "sed -n \"$(rm x)p\" f",
		"cp -a src/. dst", "cp -r . ../backup", "cp -b a b", "cp --backup=numbered a b",
		"git rm '*.go'", "git rm ':(glob)*.go'", "git mv '*.go' dst",
		"sed -n ':a;w out' f", "sed --follow-symlinks -i s/a/b/ f", "sed --unknown -i s/a/b/ f",
		"sort --compress-program=gzip -o out in", "fd -x rm", "fd --exec rm", "rg --pre ./x foo",
		"tree -o out", "less -o log a", "less '+!rm x' a", "hg cat -o out a", "git -c core.pager=x log",
		"git config core.hooksPath x", "git grep -O foo", "gawk -p '{}' f", "awk -f prog.awk f",
		"shopt -s globstar; rm **/*.log", "alias rm=true; rm a", "hash -p /tmp/x rm; rm a",
		"PATH=/tmp:$PATH rm a", "export PATH=/tmp; rm a", "env PATH=/tmp rm a", "./rm a", "bin/cp a b",
		"rm [[:alpha:]]*", "rm **/*.log",
	} {
		if plan := classifyExecShell(command, workdir, "bash"); plan.Class != execOpaque {
			t.Errorf("%q class = %v, want opaque", command, plan.Class)
		}
	}
	// Code in a substitution leaves perl -i scoped rather than declared.
	for _, command := range []string{"bash -c 'rm a'", "perl -pi -e 's/a/${\\ `rm x`}/' f", "perl -pi -e 's/(?{ system 1 })//' f"} {
		if plan := classifyExecShell(command, workdir, "bash"); plan.Class != execScoped {
			t.Errorf("%q class = %v, want scoped", command, plan.Class)
		}
	}
	for _, shell := range []string{"/usr/bin/fish", "zsh", ""} {
		if plan := classifyExecShell("rm a", workdir, shell); plan.Class != execOpaque {
			t.Errorf("shell %q class = %v, want opaque", shell, plan.Class)
		}
	}
}
