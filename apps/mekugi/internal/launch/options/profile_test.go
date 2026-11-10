package options

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestProfileImportTypedValuesAndPrecedence(t *testing.T) {
	home := t.TempDir()
	body := `model = "opaque:model"
model_context_window = 123456
model_auto_compact_token_limit = 123000
model_reasoning_effort = "high"
developer_instructions = "a\nb\t\"quoted\""
[model_providers.custom]
name = "custom"
base_url = "http://localhost:1234/prefix"
query_params = { "literal.key" = "a%2Bb" }
auth = { command = "token", args = ["--literal", "space value"], timeout_ms = 1234 }
[tui.presets."1"]
input = "hello"
`
	path := filepath.Join(home, "work.config.toml")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	profile, args, err := ImportProfile([]string{"exec", "-pwork", "-c", `model="explicit"`, "--", "--profile=literal"}, []string{"CODEX_HOME=" + home})
	if err != nil {
		t.Fatal(err)
	}
	if profile.Name != "work" || profile.Home != home {
		t.Fatalf("wrong identity: %+v", profile)
	}
	var expected map[string]any
	if _, err := toml.Decode(body, &expected); err != nil {
		t.Fatal(err)
	}
	expected["model"] = "explicit"
	actual := ConfigOverrides(args)
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("typed round trip: got %#v; want %#v", actual, expected)
	}
	if after, _ := os.ReadFile(path); string(after) != body {
		t.Fatal("source profile changed")
	}
	if strings.Join(args[len(args)-2:], " ") != "-- --profile=literal" {
		t.Fatalf("delimiter contents changed: %q", args)
	}
}

func TestProfileSelectorFormsOperandsAndRejections(t *testing.T) {
	for _, selector := range [][]string{{"-p", "work"}, {"--profile", "work"}, {"-pwork"}, {"-p=work"}, {"--profile=work"}} {
		input := append([]string{"exec", "--model", "-poperand", "-c", "--profile=operand"}, selector...)
		input = append(input, "--", "-p", "literal")
		name, args, err := ProfileSelector(input)
		want := []string{"exec", "--model", "-poperand", "-c", "--profile=operand", "--", "-p", "literal"}
		if err != nil || name != "work" || !reflect.DeepEqual(args, want) {
			t.Fatalf("%q: %q %q %v", input, name, args, err)
		}
	}
	for _, input := range [][]string{{"-p"}, {"-p", ""}, {"--profile="}, {"-p../x"}, {"-pwork.toml"}, {"-pwork", "-pwork"}, {"-p工作"}} {
		if _, _, err := ProfileSelector(input); err == nil {
			t.Fatalf("accepted invalid selector: %q", input)
		}
	}
}

func TestProfileImportContentFreeFailures(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "work.config.toml")
	for _, body := range []string{"", "model = ['SENSITIVE", "[nested\nsecret='SENSITIVE'"} {
		if body != "" {
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
		}
		_, _, err := ImportProfile([]string{"-pwork"}, []string{"CODEX_HOME=" + home})
		if err == nil || strings.Contains(err.Error(), "SENSITIVE") {
			t.Fatalf("missing/private error: %v", err)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ImportProfile([]string{"-pwork"}, []string{"CODEX_HOME=" + home}); err == nil {
		t.Fatal("accepted directory profile")
	}
}
