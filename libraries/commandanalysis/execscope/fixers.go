package execscope

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yusing/mekugi/libraries/sourceanalysis/sourceparse"
)

// execGoFormatterWrites is shared by capture and display-only segment tracking.
// GoFormatterWrites identifies formatter invocations whose write segment
// determines a preview lifetime.
func GoFormatterWrites(identity string, args []string) bool {
	return (identity == "gofmt" || identity == "goimports") && slices.Contains(args, "-w")
}

func execFormatterIdentity(identity string) bool {
	switch identity {
	case "gofmt", "goimports", "prettier", "eslint", "ruff", "black", "rustfmt", "bun fmt":
		return true
	}
	return false
}

func execFixerScope(input execProviderInput) execProviderResult {
	if input.authoredOnly && !execFormatterIdentity(input.identity) {
		return execProviderResult{unhandled: true}
	}
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
	installsDependencies := false
	root := input.cwd
	switch input.identity {
	case "gofmt", "goimports":
		if !GoFormatterWrites(input.identity, args) {
			return execProviderResult{unhandled: true}
		}
		extensions = []string{".go"}
	case "prettier":
		if !has("--write") {
			return execProviderResult{unhandled: true}
		}
		extensions = []string{".js", ".jsx", ".ts", ".tsx", ".json", ".css", ".scss", ".html", ".md", ".yaml", ".yml", ".vue", ".svelte"}
	case "bun fmt":
		// Check/output-only forms do not establish named write scope.
		if has("--check") || has("-c") || has("--list-different") || has("-l") || has("--list-files") || has("--debug-check") || has("--doctor") || has("--stdin-filepath") || has("--init") || has("--cwd") || slices.ContainsFunc(args, func(arg string) bool {
			return strings.HasPrefix(arg, "--stdin-filepath=") || strings.HasPrefix(arg, "--cwd=")
		}) {
			return execProviderResult{unhandled: true}
		}
		extensions = []string{".js", ".jsx", ".ts", ".tsx", ".json", ".jsonc", ".css", ".scss", ".html", ".md", ".mdx", ".yaml", ".yml", ".vue", ".svelte"}
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
		if first == "fix" {
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
		installsDependencies = true
		if first != "" && !slices.Contains([]string{"install", "ci", "i", "add", "remove", "rm", "update", "upgrade", "uninstall"}, first) {
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
			operands = append(operands, sourceparse.Path(root, arg))
		}
		if input.authoredOnly {
			// Named formatter targets are source edits. A default directory,
			// permission root, or recursive formatter scan is not authored scope.
			for _, path := range operands {
				if info, err := os.Stat(path); err == nil && !info.IsDir() {
					paths = append(paths, path)
				}
			}
			if len(paths) == 0 {
				return execProviderResult{unhandled: true}
			}
			return execProviderResult{scope: []Entry{execProviderFiles(paths, false)}}
		}
		if len(operands) == 0 {
			operands = []string{root}
		}
		walker := execPathWalker{remaining: ListingLimit, deadline: input.deadline}
		for _, operand := range operands {
			matches := []string{operand}
			if strings.ContainsAny(operand, "*?[") {
				var err error
				matches, err = walker.glob(operand)
				truncated = truncated || errors.Is(err, errExecWalkIncomplete)
			}
			for _, path := range matches {
				if info, err := os.Lstat(path); err == nil && !info.IsDir() {
					paths = append(paths, path)
					continue
				}
				err := walker.walk(path, func(path string, entry fs.DirEntry, err error) error {
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
				if err != nil {
					truncated = true
				}
			}
		}
	}
	entry := execProviderFiles(paths, false)
	entry.Origin = strings.TrimSpace(label)
	result := execProviderResult{scope: []Entry{entry}, open: truncated, opaque: installsDependencies}
	if installsDependencies {
		result.reason = "dependency installation may run lifecycle scripts"
	}
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
