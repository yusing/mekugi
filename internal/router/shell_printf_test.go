package router

import (
	"reflect"
	"strings"
	"testing"
)

func TestPrintfQuoteFormat(t *testing.T) {
	tests := []struct {
		name       string
		format     string
		wantFormat string
		wantArgs   []int
	}{
		{"plain", "%q", "%s", []int{0}},
		{"mixed", "%s:%q:%d", "%s:%s:%d", []int{1}},
		{"flags and percent", "%% %-10q %+d", "%% %-10s %+d", []int{0}},
		{"escaped percent", `\%q %q`, `\%q %s`, []int{0}},
		{"no quote", "%s", "%s", nil},
		{"trailing percent", "%q%", "%s%", []int{0}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotFormat, gotArgs := printfQuoteFormat(test.format)
			if gotFormat != test.wantFormat {
				t.Errorf("format = %q, want %q", gotFormat, test.wantFormat)
			}
			if !reflect.DeepEqual(gotArgs, test.wantArgs) {
				t.Errorf("argument positions = %v, want %v", gotArgs, test.wantArgs)
			}
		})
	}
}

func TestShellPrintfQuoteAndFormatReuse(t *testing.T) {
	stdout, stderr, status := runShellWorkerTest(t, sharedProxyTestRegistry(t), "bash", nil,
		`printf '%q|%s|%%\n' 'a b' x 'c$d' y`, nil)
	if stdout != `'a b'|x|%
'c$d'|y|%
` || stderr != "" || status != 0 {
		t.Fatalf("printf = (%q, %q, %d)", stdout, stderr, status)
	}
}

func TestShellPrintfFormatErrorDoesNotAbortScript(t *testing.T) {
	stdout, stderr, status := runShellWorkerTest(t, sharedProxyTestRegistry(t), "bash", nil,
		`printf '%q %z' value; printf marker`, nil)
	if stdout != "marker" || status != 0 {
		t.Fatalf("printf error continuation = (%q, %q, %d)", stdout, stderr, status)
	}
	if stderr != "printf: invalid format char: z\n" {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestShellPrintfQuoteRoundTripsWithoutExecution(t *testing.T) {
	script := `
values=('' $'line\nnext' 'single'\''quote' '$(printf EXECUTED)' '` + "`printf EXECUTED`" + `')
for value in "${values[@]}"; do
  quoted=$(printf '%q' "$value")
  eval "set -- $quoted"
  printf '<%s>' "$1"
done
printf '|missing=<%q>'
`
	stdout, stderr, status := runShellWorkerTest(t, sharedProxyTestRegistry(t), "bash", nil, script, nil)
	want := "<>" +
		"<line\nnext>" +
		"<single'quote>" +
		"<$(printf EXECUTED)>" +
		"<`printf EXECUTED`>" +
		"|missing=<''>"
	if stdout != want || stderr != "" || status != 0 {
		t.Fatalf("printf quote round trip = (%q, %q, %d), want stdout %q", stdout, stderr, status, want)
	}
}

func TestShellPrintfQuoteReuseScalesAcrossManyArguments(t *testing.T) {
	const count = 5000
	script := "result=$(printf '%q\\n'" + strings.Repeat(" x", count) + `); printf '%s' "${#result}"`
	stdout, stderr, status := runShellWorkerTest(t, sharedProxyTestRegistry(t), "bash", nil, script, nil)
	if stdout != "9999" || stderr != "" || status != 0 {
		t.Fatalf("large printf = (%q, %q, %d)", stdout, stderr, status)
	}
}
