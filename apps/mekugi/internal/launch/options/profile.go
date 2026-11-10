package options

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Profile is frozen invocation input, not a configuration layer or a writer.
// Values and expanded arguments must remain private to the admitted runtime.
type Profile struct {
	Name, Home string
	Keys       []string
}

// ProfileSelector removes only profile selectors, retaining operands and --.
func ProfileSelector(args []string) (string, []string, error) {
	var name string
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			out = append(out, args[i:]...)
			break
		}
		value, selected := "", false
		switch {
		case arg == "-p" || arg == "--profile":
			if i+1 == len(args) {
				return "", nil, errors.New("--profile requires a plain name")
			}
			i++
			value, selected = args[i], true
		case strings.HasPrefix(arg, "--profile="):
			value, selected = strings.TrimPrefix(arg, "--profile="), true
		case strings.HasPrefix(arg, "-p") && len(arg) > 2:
			value, selected = strings.TrimPrefix(strings.TrimPrefix(arg, "-p"), "="), true
		}
		if selected {
			if name != "" {
				return "", nil, errors.New("--profile may be supplied only once")
			}
			if value == "" || strings.ContainsFunc(value, func(r rune) bool {
				return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-')
			}) {
				return "", nil, errors.New("--profile requires a plain alphanumeric, underscore or hyphen name")
			}
			name = value
			continue
		}
		out = append(out, arg)
		if CodexOptionOperand(arg) && i+1 < len(args) {
			i++
			out = append(out, args[i])
		}
	}
	return name, out, nil
}

// CodexOptionOperand identifies separate option values, never their contents.
func CodexOptionOperand(arg string) bool {
	return slices.Contains([]string{"-c", "--config", "--enable", "--disable", "-i", "--image", "-m", "--model", "-s", "--sandbox", "-a", "--ask-for-approval", "-C", "--cd", "--add-dir", "-o", "--output-last-message", "--output-schema", "--color", "--local-provider", "--thread-source"}, arg)
}

func EnvironmentValue(environment []string, key string) string {
	for _, entry := range slices.Backward(environment) {
		if value, ok := strings.CutPrefix(entry, key+"="); ok {
			return value
		}
	}
	return ""
}

// ImportProfile reads the selected regular file once and prepends typed overrides.
// Error messages deliberately exclude TOML parser diagnostics and file contents.
func ImportProfile(args, environment []string) (*Profile, []string, error) {
	name, explicit, err := ProfileSelector(args)
	if err != nil || name == "" {
		return nil, explicit, err
	}
	home := EnvironmentValue(environment, "CODEX_HOME")
	if home == "" {
		home = filepath.Join(EnvironmentValue(environment, "HOME"), ".codex")
	}
	home, err = filepath.Abs(home)
	if err != nil {
		return nil, nil, errors.New("resolve profile Codex home")
	}
	path := filepath.Join(home, name+".config.toml")
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, nil, errors.New("cannot read regular Codex profile file")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, errors.New("cannot read Codex profile file")
	}
	var values map[string]any
	if _, err := toml.Decode(string(body), &values); err != nil {
		return nil, nil, errors.New("invalid Codex profile TOML")
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		// Codex splits override paths on dots. A literal top-level dotted key
		// cannot be represented without changing its identity.
		if strings.Contains(key, ".") {
			return nil, nil, errors.New("Codex profile has an unrepresentable top-level dotted key")
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var overrides []string
	for _, key := range keys {
		value, err := TOMLValue(values[key])
		if err != nil {
			return nil, nil, fmt.Errorf("encode Codex profile setting %s", key)
		}
		overrides = append(overrides, "-c", key+"="+value)
	}
	return &Profile{Name: name, Home: home, Keys: keys}, append(overrides, explicit...), nil
}

// TOMLValue uses the pinned encoder for primitive syntax and nested inline tables.
// Nesting in an array prevents the encoder from emitting table declarations.
func TOMLValue(value any) (string, error) {
	var encoded bytes.Buffer
	if err := toml.NewEncoder(&encoded).Encode(map[string]any{"v": []any{[]any{value}}}); err != nil {
		return "", err
	}
	raw := strings.TrimSpace(encoded.String())
	_, raw, ok := strings.Cut(raw, "=")
	raw = strings.TrimSpace(raw)
	if !ok || !strings.HasPrefix(raw, "[[") || !strings.HasSuffix(raw, "]]") {
		return "", errors.New("TOML value is not representable")
	}
	return raw[2 : len(raw)-2], nil
}

// ConfigOverrides mirrors host CLI value parsing and dotted-path replacement.
// Callers use the result only for invocation-owned admission and projection.
func ConfigOverrides(args []string) map[string]any {
	out := make(map[string]any)
	for i := 0; i < len(args) && args[i] != "--"; i++ {
		arg := args[i]
		var raw string
		switch {
		case arg == "-c" || arg == "--config":
			if i+1 < len(args) {
				i++
				raw = args[i]
			}
		case strings.HasPrefix(arg, "--config="):
			raw = strings.TrimPrefix(arg, "--config=")
		case strings.HasPrefix(arg, "-c"):
			raw = strings.TrimPrefix(strings.TrimPrefix(arg, "-c"), "=")
		case arg == "-m" || arg == "--model":
			if i+1 < len(args) {
				i++
				out["model"] = args[i]
			}
			continue
		case strings.HasPrefix(arg, "--model="):
			out["model"] = strings.TrimPrefix(arg, "--model=")
			continue
		case strings.HasPrefix(arg, "-m"):
			out["model"] = strings.TrimPrefix(strings.TrimPrefix(arg, "-m"), "=")
			continue
		default:
			if CodexOptionOperand(arg) {
				i++
			}
			continue
		}
		key, value, ok := strings.Cut(raw, "=")
		if !ok || strings.TrimSpace(key) == "" {
			continue // Codex owns malformed CLI diagnostics.
		}
		var parsed map[string]any
		var setting any
		if _, err := toml.Decode("v="+value, &parsed); err == nil {
			setting = parsed["v"]
		} else {
			setting = strings.Trim(strings.TrimSpace(value), `"'`)
		}
		parts := strings.Split(strings.TrimSpace(key), ".")
		target := out
		for _, part := range parts[:len(parts)-1] {
			child, ok := target[part].(map[string]any)
			if !ok {
				child = make(map[string]any)
				target[part] = child
			}
			target = child
		}
		target[parts[len(parts)-1]] = setting
	}
	return out
}
