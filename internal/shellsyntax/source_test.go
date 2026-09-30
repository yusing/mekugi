package shellsyntax

import (
	"os/exec"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/syntax"
)

func TestStatementSourcePreservesOrdinaryFallback(t *testing.T) {
	program, err := syntax.NewParser().Parse(strings.NewReader("printf ordinary; printf sibling\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, fallback := range []string{"printf ordinary", "printf ordinary;", "  printf ordinary;\n", ""} {
		if got := StatementSource(program.Stmts[0], fallback); got != fallback {
			t.Errorf("StatementSource fallback %q = %q", fallback, got)
		}
	}
}

func TestStatementSourceOwnsHeredoc(t *testing.T) {
	for _, test := range []struct {
		name   string
		input  string
		output string
	}{
		{
			name:   "body follows sibling",
			input:  "cat <<EOF; printf sibling\nowned body\nEOF\n",
			output: "owned body\n",
		},
		{
			name:   "quoted delimiter",
			input:  "cat <<'EOF'; printf sibling\n$(printf expanded) `printf expanded` $HOME\nEOF\n",
			output: "$(printf expanded) `printf expanded` $HOME\n",
		},
		{
			name:   "tab stripped quoted delimiter",
			input:  "cat <<-\"EOF\"; printf sibling\n\towned $HOME\n\tEOF\n",
			output: "owned $HOME\n",
		},
		{
			name:   "nested owning statement",
			input:  "{ cat <<'EOF'; } ; printf sibling\nnested body\nEOF\n",
			output: "nested body\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			program, err := syntax.NewParser().Parse(strings.NewReader(test.input), "")
			if err != nil {
				t.Fatal(err)
			}
			if len(program.Stmts) != 2 {
				t.Fatalf("fixture has %d statements, want owner and sibling", len(program.Stmts))
			}
			got := StatementSource(program.Stmts[0], "fallback must not be used")
			if strings.HasSuffix(got, "\n") {
				t.Fatalf("StatementSource retained trailing newline: %q", got)
			}
			reparsed, err := syntax.NewParser().Parse(strings.NewReader(got), "")
			if err != nil || len(reparsed.Stmts) != 1 {
				t.Fatalf("StatementSource must parse as one owning statement: %q, error %v", got, err)
			}
			output, err := exec.CommandContext(t.Context(), "sh", "-c", got).CombinedOutput()
			if err != nil {
				t.Fatalf("shell rejected StatementSource %q: %v\n%s", got, err, output)
			}
			if string(output) != test.output {
				t.Fatalf("StatementSource %q output = %q, want %q", got, output, test.output)
			}
			if sibling := StatementSource(program.Stmts[1], "printf sibling;"); sibling != "printf sibling;" {
				t.Fatalf("sibling borrowed the owner's heredoc: %q", sibling)
			}
		})
	}
}
