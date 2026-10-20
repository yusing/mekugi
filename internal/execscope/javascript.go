package execscope

import (
	json "encoding/json/v2"
	"io"
	"path/filepath"
	"strings"

	"github.com/yusing/mekugi/internal/sourceparse"
)

func execJavaScriptScope(input execProviderInput) execProviderResult {
	source, script, reason := execProgramSource(input)
	if reason != "" {
		return execProviderResult{open: true, reason: reason}
	}
	language := sourceparse.JavaScript
	if strings.HasSuffix(script, ".ts") || input.identity == "deno" {
		language = sourceparse.TypeScript
	}
	result := inspectExecSource(input, source, script, language, false)
	if input.authoredOnly {
		// Runtime permissions bound possible effects, not authored edit targets.
		return result
	}
	var roots []string
	bounded, escape := false, false
	for _, flag := range input.args {
		switch {
		case flag == "--permission":
			bounded = true
		case strings.HasPrefix(flag, "--allow-fs-write=") || strings.HasPrefix(flag, "--allow-write=") || strings.HasPrefix(flag, "-W="):
			_, paths, _ := strings.Cut(flag, "=")
			if paths == "" || paths == "*" {
				escape = true
				continue
			}
			if input.identity == "deno" {
				bounded = true
			}
			for path := range strings.SplitSeq(paths, ",") {
				if target := sourceparse.Path(input.cwd, path); target != "" {
					roots = append(roots, target)
				}
			}
		case flag == "-A" || flag == "--allow-all" || flag == "-W" || flag == "--allow-write" || strings.HasPrefix(flag, "--allow-run") || strings.HasPrefix(flag, "--allow-child-process") || strings.HasPrefix(flag, "--allow-addons") || strings.HasPrefix(flag, "--allow-wasi") || strings.HasPrefix(flag, "--allow-ffi"):
			escape = true
		case input.identity == "deno" && (flag == "-P" || flag == "--permission-set" || strings.HasPrefix(flag, "-P=") || strings.HasPrefix(flag, "--permission-set=")):
			_, set, _ := strings.Cut(flag, "=")
			if set == "" {
				set = "default"
			}
			paths, open := execDenoPermissionSet(input.cwd, set)
			roots = append(roots, paths...)
			bounded = true
			escape = escape || open
		}
	}
	if bounded {
		result.scope = append(result.scope, execProviderFiles(roots, true))
		// Dynamic source remains visibly open even with a permission hint.
		result.open = escape || strings.Contains(source, "eval(") || strings.Contains(source, "import(") || strings.Contains(source, "new Function")
		if !result.open {
			result.reason = ""
		} else {
			result.reason = "runtime permissions leave unresolved targets"
		}
	}
	return result
}

func execDenoPermissionSet(cwd, set string) ([]string, bool) {
	if !filepath.IsAbs(cwd) {
		return nil, true
	}
	file, err := sourceparse.Open(filepath.Join(cwd, "deno.json"))
	if err != nil {
		return nil, true
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, true
	}
	data, err := io.ReadAll(io.LimitReader(file, SourceLimit+1))
	if err != nil || len(data) > SourceLimit {
		return nil, true
	}
	var config struct {
		Permissions map[string]map[string]any `json:"permissions"`
	}
	if json.Unmarshal(data, &config) != nil {
		return nil, true
	}
	permissions, ok := config.Permissions[set]
	if !ok {
		return nil, true
	}
	for _, name := range []string{"run", "ffi"} {
		if value, present := permissions[name]; present && value != false {
			return nil, true
		}
	}
	var roots []string
	switch value := permissions["write"].(type) {
	case nil:
		return nil, false
	case bool:
		return nil, value
	case string:
		roots = append(roots, sourceparse.Path(cwd, value))
	case []any:
		for _, item := range value {
			path, ok := item.(string)
			if !ok {
				return nil, true
			}
			roots = append(roots, sourceparse.Path(cwd, path))
		}
	default:
		return nil, true
	}
	return roots, false
}
