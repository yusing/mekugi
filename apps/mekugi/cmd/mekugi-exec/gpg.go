package main

import (
	"bufio"
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/yusing/mekugi/backends/codex/shellapproval"
	"github.com/yusing/mekugi/libraries/shellinstrumentation"
	"github.com/yusing/mekugi/modules/sudoapproval/gpg"
	"golang.org/x/sys/unix"
)

// gpgCommand joins native GPG and its status stream before returning. GPG owns
// authentication, caching and ordinary stdio; only inquiries use private pipes.
func gpgCommand(directory, target, argv0 string, defaultPath bool, args []string) int {
	name := filepath.Base(target)
	explicit := os.Getenv(shellinstrumentation.RealEnvironment)
	os.Unsetenv(shellinstrumentation.RealEnvironment)
	if strings.Contains(target, "/") {
		explicit = target
	}
	found, real, err := guardPaths(name, explicit, defaultPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gpg:", err)
		return 127
	}
	if directory == "" {
		directory = found
	}
	options, err := gpgOptionTable(real)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mekugi: GPG option metadata unavailable; retaining native input")
	}
	forward, status, interactive := gpgArguments(args, options)
	if err != nil || !interactive {
		if unix.Exec(real, append([]string{argv0}, args...), os.Environ()) != nil {
			return 126
		}
	}
	// Keep every caller-inherited descriptor, not just stdin/stdout/stderr.
	files, err := gpgInheritedFiles()
	if err != nil {
		fmt.Fprintln(os.Stderr, "mekugi: cannot preserve GPG descriptors")
		return 1
	}
	if status >= 3 && (status-3 >= len(files) || files[status-3] == nil) {
		// A closed caller descriptor must not become one of our new pipes.
		unix.Exec(real, append([]string{argv0}, args...), os.Environ())
		return 126
	}
	statusRead, statusWrite, err := os.Pipe()
	if err != nil {
		return 1
	}
	defer statusRead.Close()
	defer statusWrite.Close()
	answerRead, answerWrite, err := os.Pipe()
	if err != nil {
		return 1
	}
	defer answerRead.Close()
	defer answerWrite.Close()
	lifetimeRead, lifetimeWrite, err := os.Pipe()
	if err != nil {
		return 1
	}
	defer lifetimeRead.Close()
	defer lifetimeWrite.Close()
	helper, err := os.Executable()
	if err != nil {
		return 1
	}
	args = append([]string{"--gpg-run", strconv.Itoa(5 + len(files)), real, argv0, "--pinentry-mode", "loopback", "--status-fd", strconv.Itoa(3 + len(files)), "--command-fd", strconv.Itoa(4 + len(files))}, forward...)
	command := exec.Command(helper, args...)
	command.ExtraFiles = append(files, statusWrite, answerRead, lifetimeRead)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop, err := startGPG(ctx, command)
	if err != nil {
		return 126
	}
	defer stop()
	statusWrite.Close()
	answerRead.Close()
	lifetimeRead.Close()
	done := make(chan error, 1)
	go func() { done <- command.Wait(); cancel() }()
	var output io.Writer = io.Discard
	if status >= 0 {
		output = os.NewFile(uintptr(status), "gpg-status")
	}
	scanner := bufio.NewScanner(statusRead)
	scanner.Buffer(make([]byte, 4096), shellapproval.MaxMessage)
	for scanner.Scan() {
		line := scanner.Text()
		if _, err := fmt.Fprintln(output, line); err != nil {
			lifetimeWrite.Close()
			break
		}
		if !strings.HasPrefix(line, "[GNUPG:] GET_") {
			continue
		}
		if line != "[GNUPG:] GET_HIDDEN passphrase.enter" {
			fmt.Fprintln(os.Stderr, "mekugi: GPG non-passphrase inquiry requires caller-owned --command-fd")
			lifetimeWrite.Close()
			continue
		}
		password, ok := gpgPassphrase(ctx, directory, real)
		if !ok {
			answerWrite.Close()
			continue
		}
		if _, err := fmt.Fprintln(answerWrite, password); err != nil {
			lifetimeWrite.Close()
		}
	}
	if scanner.Err() != nil {
		lifetimeWrite.Close()
	}
	if <-done == nil {
		return 0
	}
	return gpgExit(command.ProcessState)
}

// A private watcher owns and reaps GPG. The supervisor alone holds the lifetime
// pipe's writer, so even SIGKILL cancels a child blocked on ordinary stdin. This
// does not require a Linux-only parent-death signal or a global process registry.
func gpgRun(lifetime int, executable string, args []string) int {
	parent := os.NewFile(uintptr(lifetime), "gpg-parent")
	defer parent.Close()
	unix.CloseOnExec(lifetime)
	files, err := gpgInheritedFiles()
	if err != nil {
		return 1
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { io.Copy(io.Discard, parent); cancel() }()
	command := exec.CommandContext(ctx, executable)
	command.Args, command.ExtraFiles = args, files
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	stop, err := startGPG(ctx, command)
	if err != nil {
		return 126
	}
	defer stop()
	err = command.Wait()
	cancel()
	if err == nil {
		return 0
	}
	return gpgExit(command.ProcessState)
}

func startGPG(ctx context.Context, command *exec.Cmd) (func(), error) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, unix.SIGINT, unix.SIGQUIT, unix.SIGTERM, unix.SIGHUP)
	stop := func() { signal.Stop(signals) }
	if err := command.Start(); err != nil {
		stop()
		return nil, err
	}
	go func() {
		for {
			select {
			case sig := <-signals:
				command.Process.Signal(sig)
			case <-ctx.Done():
				return
			}
		}
	}()
	return stop, nil
}

func gpgExit(state *os.ProcessState) int {
	if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		signal.Reset(status.Signal())
		// Go retains runtime handlers for signals such as SIGQUIT and SIGPIPE
		// even after Reset. Exec restores native dispositions; the system kill
		// utility signals this same PID without Bash's builtin SIGQUIT handling.
		// Linux and macOS provide /bin/kill, independent of the guarded PATH.
		unix.Exec("/bin/kill", []string{"kill", "-" + strconv.Itoa(int(status.Signal())), strconv.Itoa(os.Getpid())}, nil)
		return 128 + int(status.Signal())
	}
	return state.ExitCode()
}

func gpgInheritedFiles() ([]*os.File, error) {
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		return nil, err
	}
	var files []*os.File
	for _, entry := range entries {
		fd, err := strconv.Atoi(entry.Name())
		if err != nil || fd < 3 {
			continue
		}
		flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		if err != nil || flags&unix.FD_CLOEXEC != 0 {
			continue
		}
		for len(files) <= fd-3 {
			files = append(files, nil)
		}
		files[fd-3] = os.NewFile(uintptr(fd), "inherited-gpg-fd")
	}
	return files, nil
}

func gpgPassphrase(ctx context.Context, directory, executable string) (string, bool) {
	socket, err := os.Readlink(shellapproval.ChannelOf(directory))
	if err != nil {
		return "", false
	}
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", socket)
	if err != nil {
		return "", false
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if conn.SetDeadline(time.Now().Add(shellapproval.Timeout+5*time.Second)) != nil {
		return "", false
	}
	cwd, _ := os.Getwd()
	message := shellapproval.Message{Kind: "gpg-passphrase", Thread: os.Getenv("CODEX_THREAD_ID"), Item: os.Getenv(shellinstrumentation.ItemEnvironment), Cwd: cwd, Executable: executable, Argv: []string{executable}}
	if json.MarshalWrite(conn, &message) != nil {
		return "", false
	}
	var reply shellapproval.Reply
	if json.UnmarshalRead(io.LimitReader(conn, shellapproval.MaxMessage), &reply) != nil || !reply.OK || !gpg.ValidPassphrase(reply.Password) {
		return "", false
	}
	return reply.Password, true
}
