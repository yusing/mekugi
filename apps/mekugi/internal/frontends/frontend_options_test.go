package frontends

import (
	"reflect"
	"testing"

	"github.com/yusing/mekugi/libraries/commandargs"
)

func TestFrontendInlineBudgets(t *testing.T) {
	workspace := t.TempDir()
	parsers := map[string]func([]string) (int, error){
		"mcat": func(args []string) (int, error) {
			_, options, err := commandargs.ParseReadBundle(append(args, "file.go"))
			return options.Budget, err
		},
		"mchanges": func(args []string) (int, error) {
			options, err := testChanges().Parse(append(args, "amber1"), workspace)
			return options.MaxTokens, err
		},
	}
	for name, parse := range parsers {
		t.Run(name, func(t *testing.T) {
			for _, args := range [][]string{{"--max-tokens=42"}, {"--max-tokens", "42"}} {
				if got, err := parse(args); err != nil || got != 42 {
					t.Fatalf("%q: budget=%d err=%v", args, got, err)
				}
			}
			for _, args := range [][]string{{"--max-tokens="}, {"--max-tokens=0"}, {"--max-tokens=01"}, {"--max-tokens=15501"}, {"--max-tokens=x"}, {"--max-tokens=4", "--max-tokens", "5"}} {
				if _, err := parse(args); err == nil || err.Error() != commandargs.MaxTokensArgumentError {
					t.Fatalf("%q: err=%v", args, err)
				}
			}
		})
	}
	options, err := testChanges().Parse([]string{"amber1", "--", "--max-tokens=99"}, workspace)
	if err != nil || options.MaxTokens != 4000 || !reflect.DeepEqual(options.Paths, []string{"--max-tokens=99"}) {
		t.Fatalf("path boundary: %+v %v", options, err)
	}
}

func TestFrontendDistinctOperandPolicies(t *testing.T) {
	if _, _, err := commandargs.ParseReadBundle([]string{"source", "1:9007199254740992"}); err == nil {
		t.Fatal("mcat accepted a range above the safe-integer ceiling")
	}

}
