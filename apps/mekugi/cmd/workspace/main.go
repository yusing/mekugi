// Command workspace runs contributor tools over the members declared in go.work.
package main

import (
	json "encoding/json/v2"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const prefix = "github.com/yusing/mekugi"

type member struct{ directory, path string }

func main() {
	if err := run(os.Args[1:]); err != nil {
		if status, ok := errors.AsType[*exec.ExitError](err); ok {
			os.Exit(status.ExitCode())
		}
		fmt.Fprintln(os.Stderr, "workspace:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("expected check or run COMMAND [ARGS]; @workspace expands to member package patterns")
	}
	members, err := workspaceMembers()
	if err != nil {
		return err
	}
	if args[0] == "check" {
		return checkImports(members)
	}
	if args[0] != "run" || len(args) < 2 {
		return errors.New("expected check or run COMMAND [ARGS]")
	}
	command := exec.Command(args[1], expand(args[2:], members)...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	return command.Run()
}

func workspaceMembers() ([]member, error) {
	data, err := exec.Command("go", "work", "edit", "-json").Output()
	if err != nil {
		return nil, fmt.Errorf("read go.work: %w", err)
	}
	var work struct{ Use []struct{ DiskPath string } }
	if err := json.Unmarshal(data, &work); err != nil {
		return nil, err
	}
	if len(work.Use) == 0 {
		return nil, errors.New("go.work has no members")
	}
	members := make([]member, 0, len(work.Use))
	for _, use := range work.Use {
		directory := filepath.Clean(use.DiskPath)
		if filepath.IsAbs(directory) || directory == ".." || strings.HasPrefix(directory, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("workspace member outside repository: %s", directory)
		}
		data, err := os.ReadFile(filepath.Join(directory, "go.mod"))
		if err != nil {
			return nil, err
		}
		path := ""
		for line := range strings.SplitSeq(string(data), "\n") {
			if fields := strings.Fields(line); len(fields) == 2 && fields[0] == "module" {
				path = strings.Trim(fields[1], `"`)
				break
			}
		}
		want := prefix + "/" + filepath.ToSlash(directory)
		if path != want {
			return nil, fmt.Errorf("%s: module path %q, want %q", directory, path, want)
		}
		members = append(members, member{directory, path})
	}
	return members, nil
}

func expand(args []string, members []member) []string {
	var result []string
	for _, arg := range args {
		if arg != "@workspace" {
			result = append(result, arg)
			continue
		}
		for _, m := range members {
			result = append(result, "./"+filepath.ToSlash(m.directory)+"/...")
		}
	}
	return result
}

func checkImports(members []member) error {
	var failures []error
	for _, m := range members {
		if strings.HasPrefix(filepath.ToSlash(m.directory), "modules/") {
			if err := checkRequires(m); err != nil {
				failures = append(failures, err)
			}
		}
		err := filepath.WalkDir(m.directory, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if path != m.directory {
					if strings.HasPrefix(entry.Name(), ".") || entry.Name() == "testdata" || entry.Name() == "vendor" {
						return filepath.SkipDir
					}
					if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
						return filepath.SkipDir
					}
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, imported := range file.Imports {
				name, err := strconv.Unquote(imported.Path.Value)
				if err != nil {
					return err
				}
				owner, _ := filepath.Rel(m.directory, filepath.Dir(path))
				if err := allowedImport(m.directory, filepath.ToSlash(owner), name); err != nil {
					failures = append(failures, fmt.Errorf("%s: %w", path, err))
				}
			}
			return nil
		})
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func allowedImport(directory, pkg, imported string) error {
	owner := filepath.ToSlash(directory)
	target := strings.TrimPrefix(imported, prefix+"/")
	local := imported == prefix || target != imported
	deny := func() error { return fmt.Errorf("%s cannot import %s", owner, imported) }
	if strings.HasPrefix(target, "testsupport/") && !strings.HasPrefix(owner, "testsupport/") {
		return deny()
	}
	if owner == "apps/mekugi" {
		area, _, _ := strings.Cut(strings.TrimPrefix(pkg, "internal/"), "/")
		neutral := area == "sessioncontrol" || area == "sessionpresentation" || area == "headless"
		if neutral && (terminalImport(imported) || strings.HasPrefix(target, "modules/") && strings.Contains(target, "/terminalview")) {
			return deny()
		}
		if area == "sessionpresentation" && strings.HasPrefix(target, "backends/") {
			return deny()
		}
		if rest, ok := strings.CutPrefix(target, owner+"/internal/"); ok {
			dependency, _, _ := strings.Cut(rest, "/")
			if neutral && (dependency == "router" || dependency == "terminalsession") ||
				area == "headless" && dependency == "sessionpresentation" ||
				area == "frontends" && slices.Contains([]string{"router", "composition", "launch"}, dependency) {
				return deny()
			}
		}
		return nil
	}
	if strings.HasPrefix(owner, "apps/") || strings.HasPrefix(owner, "testsupport/") {
		return nil
	}
	if owner == "contracts" {
		if local && target != "contracts" && !strings.HasPrefix(target, "contracts/") {
			return deny()
		}
		return nil
	}
	if strings.HasPrefix(owner, "libraries/") {
		if !local || target == owner || strings.HasPrefix(target, owner+"/") {
			return nil
		}
		allowed := map[string][]string{
			"commandanalysis":      {"sourceanalysis", "commandargs"},
			"shellinstrumentation": {"commandanalysis", "commandargs"},
			"outputbudget":         {"sourceanalysis"},
			"diffrender":           {"diffmodel", "pathdisplay", "textsanitize"},
			"terminalui":           {"diffrender", "pathdisplay", "sourceanalysis", "textsanitize"},
		}
		library, _, _ := strings.Cut(strings.TrimPrefix(target, "libraries/"), "/")
		if strings.HasPrefix(target, "libraries/") && slices.Contains(allowed[strings.TrimPrefix(owner, "libraries/")], library) {
			return nil
		}
		return deny()
	}
	if strings.HasPrefix(owner, "backends/") || strings.HasPrefix(owner, "providers/") {
		if local && target != owner && !strings.HasPrefix(target, owner+"/") && !strings.HasPrefix(target, "contracts/") && !strings.HasPrefix(target, "libraries/") {
			return deny()
		}
		return nil
	}
	if strings.HasPrefix(owner, "modules/") {
		if pkg != "." && pkg != "terminalview" && terminalImport(imported) {
			return deny()
		}
		if pkg != "." && pkg != "codexbinding" && pkg != "terminalview" && (target == owner || strings.HasPrefix(target, owner+"/codexbinding") || strings.HasPrefix(target, owner+"/terminalview")) {
			return deny()
		}
		if !local || target == owner || strings.HasPrefix(target, owner+"/") || strings.HasPrefix(target, "contracts/") || strings.HasPrefix(target, "libraries/") {
			return nil
		}
		if pkg == "codexbinding" && target == "backends/codex/binding" {
			return nil
		}
		if strings.HasPrefix(target, "modules/") {
			parts := strings.Split(target, "/")
			if len(parts) < 3 || slices.Contains([]string{"internal", "codexbinding", "terminalview", "presentation"}, parts[2]) {
				return deny()
			}
			return nil // Requires is checked against the descriptor and typed Deps.
		}
		return deny()
	}
	return fmt.Errorf("unrecognized workspace owner %q", owner)
}

func terminalImport(path string) bool {
	return strings.HasPrefix(path, prefix+"/libraries/terminalui") ||
		strings.HasPrefix(path, prefix+"/libraries/diffrender") ||
		strings.HasPrefix(path, "github.com/charmbracelet/") || path == "golang.org/x/term"
}
