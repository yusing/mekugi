package main

import (
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/internal/vcsguard"
	"golang.org/x/sys/unix"
)

// guard runs a version-control command in place of the guard's PATH entry.
// Every invocation runs the real tool by exec, keeping its arguments, argv[0],
// environment, descriptors and exit status. A remote write first waits for
// the router's approval; a denial, timeout or unreachable router exits 1
// without running it, so only this command fails and the shell continues.
func guard(name string, args []string) int {
	explicit := os.Getenv(vcsguard.RealEnvironment)
	os.Unsetenv(vcsguard.RealEnvironment) // Neither the tool nor its children inherit it.
	argv0 := os.Args[0]
	if explicit != "" {
		argv0 = explicit
	}
	return guardCommand("", name, explicit, argv0, false, args)
}

// guardCommand also serves host-instrumented executable words. The target is
// expanded by the native shell, and the channel is an explicit session path,
// so env -i and login sh do not depend on inherited guard variables or PATH.
func guardCommand(directory, name, explicit, argv0 string, defaultPath bool, args []string) int {
	foundDirectory, real, err := guardPaths(name, explicit, defaultPath)
	if directory == "" {
		directory = foundDirectory
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
		return 127
	}
	argv := append([]string{name}, args...)
	lookup := func(globals []string, alias string) (string, bool) {
		output, err := exec.Command(real, slices.Concat(globals, []string{"config", "--get", "alias." + alias})...).Output()
		if err != nil {
			return "", false
		}
		return strings.TrimSuffix(string(output), "\n"), true
	}
	if vcsguard.Writes(argv, lookup) {
		if ok, reason := approve(directory, real, argv); !ok {
			fmt.Fprintf(os.Stderr, "mekugi: remote write denied: %s\n", reason)
			return 1
		}
	}
	err = unix.Exec(real, append([]string{argv0}, args...), os.Environ())
	fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
	return 126
}

// guardPaths finds the guard directory, which holds this executable under
// the tool's name, and the real tool: the absolute path a shell function
// passes for a command that named it, or else the first other match in PATH.
// Another session's guard, which a nested Mekugi may leave behind this one,
// is not the real tool: two guards would otherwise exec each other forever.
func guardPaths(name, explicit string, defaultPath bool) (directory, real string, err error) {
	self, err := os.Executable()
	if err != nil {
		return "", "", err
	}
	selfInfo, err := os.Stat(self)
	if err != nil {
		return "", "", err
	}
	if strings.Contains(os.Args[0], "/") {
		directory = filepath.Dir(os.Args[0])
	}
	if explicit != "" {
		info, err := os.Stat(explicit)
		if filepath.Base(explicit) != name || err != nil || !info.Mode().IsRegular() {
			return "", "", fmt.Errorf("%s: command not found", explicit)
		}
		if !os.SameFile(info, selfInfo) {
			return directory, explicit, nil
		}
		// A path into another session's guard: find the real tool as usual.
	}
	pathList, pathSet := os.LookupEnv("PATH")
	if !pathSet {
		pathList = "/bin:/usr/bin" // execvp's default when env -i omits PATH.
	}
	if defaultPath {
		// command -p changes lookup, not the environment inherited by the tool.
		output, err := exec.Command("/usr/bin/getconf", "PATH").Output()
		if err != nil {
			return "", "", fmt.Errorf("resolve standard utility path: %w", err)
		}
		pathList = strings.TrimSpace(string(output))
	}
	for _, entry := range filepath.SplitList(pathList) {
		if entry == "" {
			entry = "."
		}
		candidate := filepath.Join(entry, name)
		info, err := os.Stat(candidate)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			continue
		}
		if os.SameFile(info, selfInfo) {
			if directory == "" {
				directory = entry
			}
			continue
		}
		if filepath.Base(entry) == vcsguard.Directory {
			continue
		}
		if !strings.Contains(candidate, "/") {
			candidate = "./" + candidate
		}
		return directory, candidate, nil
	}
	return "", "", errors.New("command not found")
}

// approve uses one local connection per command. Sandboxed execution is unsupported.
func approve(directory, executable string, argv []string) (bool, string) {
	const unavailable = "Mekugi approval is unavailable"
	if directory == "" {
		return false, unavailable
	}
	// The router owns the decision timeout; allow its denial to arrive before
	// the client closes and would otherwise look like a withdrawn request.
	deadline := time.Now().Add(vcsguard.Timeout + 5*time.Second)
	socket, err := os.Readlink(vcsguard.ChannelOf(directory))
	if err != nil {
		return false, unavailable
	}
	conn, err := net.DialTimeout("unix", socket, time.Second)
	if err != nil {
		return false, unavailable
	}
	defer conn.Close()
	if err := conn.SetDeadline(deadline); err != nil {
		return false, unavailable
	}
	cwd, _ := os.Getwd()
	message := vcsguard.Message{Thread: os.Getenv("CODEX_THREAD_ID"), Cwd: cwd, Argv: argv, Executable: executable}
	data, err := json.Marshal(&message)
	if err != nil || len(data) > vcsguard.MaxMessage {
		return false, "invalid approval request"
	}
	if _, err := conn.Write(data); err != nil {
		return false, unavailable
	}
	var reply vcsguard.Reply
	if err := json.UnmarshalRead(io.LimitReader(conn, vcsguard.MaxMessage), &reply); err != nil {
		return false, "no valid answer from Mekugi"
	}
	if !reply.OK && reply.Reason == "" {
		reply.Reason = "denied"
	}
	return reply.OK, reply.Reason
}
