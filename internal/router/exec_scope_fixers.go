package router

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

func execFixerScope(input execProviderInput) execProviderResult {
	if !filepath.IsAbs(input.cwd) {
		return execProviderResult{open: true, reason: "fixer working directory unavailable"}
	}
	args := input.args
	has := func(value string) bool { return slices.Contains(args, value) }
	first := ""
	if len(args) > 0 {
		first = args[0]
	}
	label := input.identity
	var extensions, manifests []string
	root := input.cwd
	switch input.identity {
	case "gofmt", "goimports":
		if !has("-w") {
			return execProviderResult{unhandled: true}
		}
		extensions = []string{".go"}
	case "prettier":
		if !has("--write") {
			return execProviderResult{unhandled: true}
		}
		extensions = []string{".js", ".jsx", ".ts", ".tsx", ".json", ".css", ".scss", ".html", ".md", ".yaml", ".yml", ".vue", ".svelte"}
	case "eslint":
		if !has("--fix") {
			return execProviderResult{unhandled: true}
		}
		extensions = []string{".js", ".jsx", ".ts", ".tsx"}
	case "ruff":
		if first != "format" && !(first == "check" && has("--fix")) {
			return execProviderResult{unhandled: true}
		}
		extensions = []string{".py", ".pyi"}
		args = args[1:]
		label += " " + first
	case "black":
		extensions = []string{".py", ".pyi"}
	case "rustfmt":
		extensions = []string{".rs"}
	case "cargo":
		if first == "fmt" {
			extensions = []string{".rs"}
			args = nil
		} else if first == "update" {
			manifests = []string{"Cargo.lock"}
			root = execNearestManifest(root, "Cargo.toml")
		} else {
			return execProviderResult{unhandled: true}
		}
		label += " " + first
	case "go":
		if first == "test" || first == "generate" || first == "fix" {
			return execGoPackageScope(input)
		}
		if first != "get" && !(first == "mod" && len(args) > 1 && args[1] == "tidy") {
			return execProviderResult{unhandled: true}
		}
		root = execNearestManifest(root, "go.mod")
		manifests = []string{"go.mod", "go.sum"}
		label += " " + first
		if first == "mod" {
			label += " tidy"
		}
	case "npm", "pnpm", "yarn", "bun":
		if first != "" && !slices.Contains([]string{"install", "i", "add", "remove", "rm", "update", "upgrade", "uninstall"}, first) {
			return execProviderResult{unhandled: true}
		}
		root = execNearestManifest(root, "package.json")
		manifests = []string{"package.json"}
		switch input.identity {
		case "npm":
			manifests = append(manifests, "package-lock.json", "npm-shrinkwrap.json")
		case "pnpm":
			manifests = append(manifests, "pnpm-lock.yaml")
		case "yarn":
			manifests = append(manifests, "yarn.lock")
		case "bun":
			manifests = append(manifests, "bun.lock", "bun.lockb")
		}
		label += " " + first
	case "uv":
		if !slices.Contains([]string{"lock", "sync", "add"}, first) {
			return execProviderResult{unhandled: true}
		}
		root = execNearestManifest(root, "pyproject.toml")
		manifests = []string{"uv.lock", "pyproject.toml"}
		label += " " + first
	case "poetry":
		if first != "lock" {
			return execProviderResult{unhandled: true}
		}
		root = execNearestManifest(root, "pyproject.toml")
		manifests = []string{"poetry.lock", "pyproject.toml"}
		label += " " + first
	default:
		return execProviderResult{unhandled: true}
	}
	var paths []string
	truncated := false
	if len(manifests) > 0 {
		for _, name := range manifests {
			paths = append(paths, filepath.Join(root, name))
		}
	} else {
		var operands []string
		for i := 0; i < len(args); i++ {
			arg := args[i]
			if strings.HasPrefix(arg, "-") {
				if slices.Contains([]string{"--config", "--ignore-path", "--stdin-filepath", "--line-length", "--target-version", "--select", "--ignore", "--extension", "-l"}, arg) && i+1 < len(args) {
					i++
				}
				continue
			}
			operands = append(operands, execProviderPath(root, arg))
		}
		if len(operands) == 0 {
			operands = []string{root}
		}
		entries := 0
		for _, operand := range operands {
			matches := []string{operand}
			if strings.ContainsAny(operand, "*?[") {
				matches, _ = filepath.Glob(operand)
			}
			for _, path := range matches {
				if info, err := os.Lstat(path); err == nil && !info.IsDir() {
					paths = append(paths, path)
					continue
				}
				_ = filepath.WalkDir(path, func(path string, entry fs.DirEntry, err error) error {
					entries++
					if entries > maxExecListingEntries || time.Now().After(input.deadline) {
						truncated = true
						return filepath.SkipAll
					}
					if err != nil {
						return nil
					}
					if entry.IsDir() {
						if path != operand && execBuiltinPruned(path, entry.Name()) {
							return filepath.SkipDir
						}
						return nil
					}
					if slices.Contains(extensions, strings.ToLower(filepath.Ext(path))) {
						paths = append(paths, path)
					}
					return nil
				})
			}
		}
	}
	entry := execProviderFiles(paths, false)
	entry.Origin = strings.TrimSpace(label)
	result := execProviderResult{scope: []execScopeEntry{entry}, open: truncated}
	if truncated {
		result.reason = "formatter scope exceeds its enumeration budget"
	}
	return result
}

func execNearestManifest(directory, name string) string {
	for current := directory; filepath.IsAbs(current); current = filepath.Dir(current) {
		if info, err := os.Stat(filepath.Join(current, name)); err == nil && info.Mode().IsRegular() {
			return current
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	return directory
}
