package router

import (
	"reflect"
	"testing"
)

func TestFrontendInlineBudgets(t *testing.T) {
	workspace := t.TempDir()
	parsers := map[string]func([]string) (int, error){
		"mcat": func(args []string) (int, error) {
			_, budget, err := parseReadBundle(append(args, "file.go"))
			return budget, err
		},
		"mread": func(args []string) (int, error) {
			options, err := parseOutputRead(append(args, "amber"))
			return options.maxTokens, err
		},
		"mchanges": func(args []string) (int, error) {
			options, err := parseChangeRead(append(args, "amber1"), workspace)
			return options.maxTokens, err
		},
		"mrun": func(args []string) (int, error) {
			options, _, err := parseMRunArguments(append(args, "echo", "hello"))
			return options.maxTokens, err
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
				if _, err := parse(args); err == nil || err.Error() != maxTokensArgumentError {
					t.Fatalf("%q: err=%v", args, err)
				}
			}
		})
	}
	for _, boundary := range [][]string{{"echo"}, {"--", "echo"}} {
		args := append([]string{"--max-tokens=42"}, boundary...)
		args = append(args, "--max-tokens=99", "-n", "2")
		_, command, err := parseMRunArguments(args)
		if err != nil || !reflect.DeepEqual(command, []string{"echo", "--max-tokens=99", "-n", "2"}) {
			t.Fatalf("command boundary: %q %v", command, err)
		}
	}
	options, err := parseChangeRead([]string{"amber1", "--", "--max-tokens=99"}, workspace)
	if err != nil || options.maxTokens != 4000 || !reflect.DeepEqual(options.paths, []string{"--max-tokens=99"}) {
		t.Fatalf("path boundary: %+v %v", options, err)
	}
}
