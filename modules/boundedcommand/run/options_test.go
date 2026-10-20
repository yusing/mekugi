package run

import (
	"reflect"
	"testing"

	"github.com/yusing/mekugi/libraries/commandargs"
)

func TestCommandArgumentPolicy(t *testing.T) {
	for _, args := range [][]string{{"--max-tokens=42"}, {"--max-tokens", "42"}} {
		got, _, err := parseMRunArguments(append(args, "echo"))
		if err != nil || got.maxTokens != 42 {
			t.Fatalf("%q: %+v %v", args, got, err)
		}
	}
	for _, args := range [][]string{{"--max-tokens="}, {"--max-tokens=0"}, {"--max-tokens=01"}, {"--max-tokens=15501"}, {"--max-tokens=x"}, {"--max-tokens=4", "--max-tokens", "5"}} {
		if _, _, err := parseMRunArguments(append(args, "echo")); err == nil || err.Error() != commandargs.MaxTokensArgumentError {
			t.Fatalf("%q: %v", args, err)
		}
	}
	for _, boundary := range [][]string{{"echo"}, {"--", "echo"}} {
		args := append([]string{"--max-tokens=42"}, boundary...)
		args = append(args, "--max-tokens=99", "-n", "2")
		_, command, err := parseMRunArguments(args)
		if err != nil || !reflect.DeepEqual(command, []string{"echo", "--max-tokens=99", "-n", "2"}) {
			t.Fatalf("command boundary: %q %v", command, err)
		}
	}
	options, _, err := parseMRunArguments([]string{"-n", "9007199254740992", "echo"})
	if err != nil || options.maxLines != 9007199254740992 {
		t.Fatalf("mrun native row-count ceiling: %+v %v", options, err)
	}
}
