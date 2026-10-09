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

	"github.com/yusing/mekugi/internal/sudoask"
	"github.com/yusing/mekugi/internal/vcsguard"
	"golang.org/x/sys/unix"
)

// sudoCommand execs sudo once. Its native authentication policy decides whether
// askpass runs; no validation probe or privileged command is replayed.
func sudoCommand(directory, target, argv0 string, defaultPath bool, args []string) int {
	explicit := ""
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
	if sudoask.CommandApproval(args) {
		if ok, reason := approve(directory, real, append([]string{target}, args...), "sudo"); !ok {
			fmt.Fprintf(os.Stderr, "mekugi: sudo command denied: %s\n", reason)
			return 1
		}
	}
	environment := os.Environ()
	if sudoask.Interactive(args) {
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
		data, err := json.Marshal(&vcsguard.Message{
			Kind: "sudo-password", Thread: os.Getenv("CODEX_THREAD_ID"), Item: os.Getenv(vcsguard.ItemEnvironment),
			Argv: argv, Executable: real,
		})
		if err != nil || len(data) > vcsguard.MaxMessage {
			return 1
		}
		environment = slices.DeleteFunc(environment, func(entry string) bool {
			return strings.HasPrefix(entry, "SUDO_ASKPASS=") || strings.HasPrefix(entry, sudoask.CommandEnvironment+"=")
		})
		environment = append(environment, "SUDO_ASKPASS="+filepath.Join(directory, sudoask.Askpass), sudoask.CommandEnvironment+"="+string(data))
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
	var message vcsguard.Message
	if json.Unmarshal([]byte(os.Getenv(sudoask.CommandEnvironment)), &message) != nil || message.Kind != "sudo-password" || len(message.Argv) == 0 {
		return 1
	}
	directory := filepath.Dir(os.Args[0])
	socket, err := os.Readlink(vcsguard.ChannelOf(directory))
	if err != nil {
		return 1
	}
	conn, err := net.DialTimeout("unix", socket, time.Second)
	if err != nil {
		return 1
	}
	defer conn.Close()
	if conn.SetDeadline(time.Now().Add(vcsguard.Timeout+5*time.Second)) != nil {
		return 1
	}
	message.Cwd, _ = os.Getwd()
	if json.MarshalWrite(conn, &message) != nil {
		return 1
	}
	var reply vcsguard.Reply
	if json.UnmarshalRead(io.LimitReader(conn, vcsguard.MaxMessage), &reply) != nil || !reply.OK || strings.ContainsAny(reply.Password, "\r\n") {
		return 1
	}
	if _, err := fmt.Fprintln(os.Stdout, reply.Password); err != nil {
		return 1
	}
	return 0
}
