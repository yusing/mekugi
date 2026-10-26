package sessioncontrol

import (
	"fmt"
	"strings"
	"unicode"
)

// ParseModelCommand translates inline choices into one host settings update.
// Explicit values remain host-validated, including custom provider models/tiers.
// Source: codex-rs/tui/src/chatwidget/model_command.rs:23:35@0df4aa677.
func ParseModelCommand(args string) (map[string]any, error) {
	model, options, err := settingsArguments(args, "--reasoning", "--speed")
	if err != nil {
		return nil, err
	}
	change := make(map[string]any)
	if model != "" {
		change["model"] = model
	}
	if effort, ok := options["--reasoning"]; ok {
		change["effort"] = effort
	}
	if speed, ok := options["--speed"]; ok {
		change["serviceTier"] = speed
		if speed == "standard" {
			change["serviceTier"] = nil
		}
	}
	if len(change) == 0 {
		return nil, fmt.Errorf("Use /model [MODEL] --reasoning=LEVEL --speed=TIER")
	}
	return change, nil
}

// settingsArguments accepts one positional value and named value options.
// Source: codex-rs/tui/src/chatwidget/slash_command_args.rs:59:114@0df4aa677.
func settingsArguments(args string, names ...string) (string, map[string]string, error) {
	words, err := settingsWords(args)
	if err != nil {
		return "", nil, err
	}
	options := make(map[string]string)
	positional, literal := "", false
	for i := 0; i < len(words); i++ {
		word := words[i]
		if !literal && word == "--" {
			literal = true
			continue
		}
		if literal || !strings.HasPrefix(word, "--") {
			if positional != "" || word == "" {
				return "", nil, fmt.Errorf("Unexpected argument %q", word)
			}
			positional = word
			continue
		}
		name, value, inline := strings.Cut(word, "=")
		known := false
		for _, allowed := range names {
			known = known || name == allowed
		}
		if !known {
			return "", nil, fmt.Errorf("Unknown setting %q", name)
		}
		if _, exists := options[name]; exists {
			return "", nil, fmt.Errorf("Setting %q was provided more than once", name)
		}
		if !inline || value == "" {
			if i+1 == len(words) || strings.HasPrefix(words[i+1], "--") || words[i+1] == "" {
				return "", nil, fmt.Errorf("Setting %q requires a value", name)
			}
			i++
			value = words[i]
		}
		options[name] = value
	}
	return positional, options, nil
}

// settingsWords follows shell quoting without evaluating expansions or commands.
func settingsWords(text string) ([]string, error) {
	var words []string
	var word strings.Builder
	var quote rune
	started, escaped := false, false
	for _, char := range text {
		if escaped {
			if quote == '"' && char != '"' && char != '\\' && char != '$' && char != '`' && char != '\n' {
				word.WriteRune('\\')
			}
			if char != '\n' {
				word.WriteRune(char)
			}
			escaped = false
		} else if char == '\\' && quote != '\'' {
			escaped, started = true, true
		} else if quote != 0 {
			if char == quote {
				quote = 0
			} else {
				word.WriteRune(char)
			}
		} else if char == '\'' || char == '"' {
			quote, started = char, true
		} else if unicode.IsSpace(char) {
			if started {
				words = append(words, word.String())
				word.Reset()
				started = false
			}
		} else {
			word.WriteRune(char)
			started = true
		}
	}
	if quote != 0 || escaped {
		return nil, fmt.Errorf("Invalid settings arguments: check the quoting")
	}
	if started {
		words = append(words, word.String())
	}
	return words, nil
}
