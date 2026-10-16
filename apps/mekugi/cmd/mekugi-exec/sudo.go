package main

import (
	json "encoding/json/v2"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/modules/sudoapproval/sudo"

	"github.com/yusing/mekugi/libraries/shellinstrumentation"

	"github.com/yusing/mekugi/backends/codex/shellapproval"
	"golang.org/x/sys/unix"
)

// sudoCommand execs sudo once. Its native authentication policy decides whether
// askpass runs; no validation probe or privileged command is replayed.
func sudoCommand(directory, target, argv0 string, defaultPath bool, args []string) int {
	explicit := ""
	if target == "sudo" {
		explicit = os.Getenv(shellinstrumentation.RealEnvironment)
		if explicit != "" {
			argv0 = explicit
		}
	}
	os.Unsetenv(shellinstrumentation.RealEnvironment) // Only this invocation uses the path function's target.
	if strings.Contains(target, "/") {
		explicit = target
	}
	found, real, err := guardPaths("sudo", explicit, defaultPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sudo:", err)
		return 127
	}
	if directory == "" {
		directory = found
	}
	if sudo.CommandApproval(args) {
		if ok, reason := approve(directory, real, append([]string{target}, args...), "sudo"); !ok {
			fmt.Fprintf(os.Stderr, "mekugi: sudo command denied: %s\n", reason)
			return 1
		}
	}
	environment := os.Environ()
	if sudo.Interactive(args) {
		// argv is display metadata, not execution input. Bound its duplicate
		// before JSON encoding so no environment string reaches execve's limit.
		argv := []string{target}
		budget := 4096
		for _, arg := range args {
			if len(arg) > budget {
				argv = append(argv, "[remaining sudo arguments omitted]")
				break
			}
			argv = append(argv, arg)
			budget -= len(arg) + 1
		}
		data, err := json.Marshal(&shellapproval.Message{
			Kind: "sudo-password", Thread: os.Getenv("CODEX_THREAD_ID"), Item: os.Getenv(shellinstrumentation.ItemEnvironment),
			Argv: argv, Executable: real,
		})
		if err != nil || len(data) > shellapproval.MaxMessage {
			return 1
		}
		environment = slices.DeleteFunc(environment, func(entry string) bool {
			return strings.HasPrefix(entry, "SUDO_ASKPASS=") || strings.HasPrefix(entry, shellapproval.CommandEnvironment+"=")
		})
		environment = append(environment, "SUDO_ASKPASS="+filepath.Join(directory, shellapproval.Askpass), shellapproval.CommandEnvironment+"="+string(data))
		args = append([]string{"-A"}, args...)
	}
	if err := unix.Exec(real, append([]string{argv0}, args...), environment); err != nil {
		fmt.Fprintln(os.Stderr, "sudo:", err)
	}
	return 126
}

// sudoPassword writes the secret only to sudo's askpass pipe. Diagnostics never
// include the reply, password, or sudo's configurable prompt.
func sudoPassword() int {
	var message shellapproval.Message
	if json.Unmarshal([]byte(os.Getenv(shellapproval.CommandEnvironment)), &message) != nil || message.Kind != "sudo-password" || len(message.Argv) == 0 {
		return 1
	}
	directory := filepath.Dir(os.Args[0])
	socket, err := os.Readlink(shellapproval.ChannelOf(directory))
	if err != nil {
		return 1
	}
	conn, err := net.DialTimeout("unix", socket, time.Second)
	if err != nil {
		return 1
	}
	defer conn.Close()
	if conn.SetDeadline(time.Now().Add(shellapproval.Timeout+5*time.Second)) != nil {
		return 1
	}
	message.Cwd, _ = os.Getwd()
	if json.MarshalWrite(conn, &message) != nil {
		return 1
	}
	var reply shellapproval.Reply
	if json.UnmarshalRead(io.LimitReader(conn, shellapproval.MaxMessage), &reply) != nil || !reply.OK || !sudo.ValidPassword(reply.Password) {
		return 1
	}
	if _, err := fmt.Fprintln(os.Stdout, reply.Password); err != nil {
		return 1
	}
	return 0
}
