package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yusing/mekugi/internal/vcsguard"
	"golang.org/x/sys/unix"
)

// A nested -c payload is available after the parent shell expands it. Rewrite
// then exec the requested shell, preserving its positional arguments and flags.
func guardShell(directory, target, argv0 string, defaultPath bool, args []string) int {
	if !slices.Contains(vcsguard.Shells, filepath.Base(target)) {
		return 1
	}
	helper, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	explicit := ""
	if strings.Contains(target, "/") {
		explicit = target
	}
	foundDirectory, real, err := guardPaths(filepath.Base(target), explicit, defaultPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 127
	}
	if directory == "" {
		directory = foundDirectory
	}
	commandString := false
	payload := -1
	for i := 0; i < len(args); i++ {
		flag := args[i]
		if flag == "--" {
			payload = i + 1
			break
		}
		if !strings.HasPrefix(flag, "-") && !strings.HasPrefix(flag, "+") {
			payload = i
			break
		}
		if flag == "-o" || flag == "-O" || flag == "--rcfile" || flag == "--init-file" {
			i++
			continue
		}
		if strings.HasPrefix(flag, "-") && !strings.HasPrefix(flag, "--") && strings.Contains(flag, "c") {
			commandString = true
		}
	}
	if commandString && payload >= 0 && payload < len(args) {
		args[payload], err = vcsguard.RewriteForItem(args[payload], helper, directory, os.Getenv(vcsguard.ItemEnvironment))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	environment := guardEnvironment(real)
	// Script files are not rewritten; their descendant PATH guards still
	// belong to this host invocation.
	if item := os.Getenv(vcsguard.ItemEnvironment); item != "" {
		environment = append(environment, vcsguard.ItemEnvironment+"="+item)
	}
	if err := unix.Exec(real, append([]string{argv0}, args...), environment); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	return 126
}
