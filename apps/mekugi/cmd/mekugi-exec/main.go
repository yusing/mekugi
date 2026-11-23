// Command mekugi-exec tracks the segments of one Codex command script. It is
// started by the Bash hook from execsegment as the command shell's coprocess,
// with the shell's original stdout and stderr on descriptors 3 and 4.
//
// It stays small so that it starts quickly: every tracked command pays for it.
package main

import (
	"bufio"
	"bytes"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/yusing/mekugi/backends/codex/shellhook"
	"github.com/yusing/mekugi/libraries/shellinstrumentation"
	segmenttransport "github.com/yusing/mekugi/modules/commandsegments/transport"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

const (
	replyTimeout = 500 * time.Millisecond // Includes the router's wait for Codex's command item.
	flushTimeout = time.Second
	// reportBudget bounds the output copied to the router. Past it, output
	// still reaches Codex unchanged, but segment output is not reported.
	reportBudget = 4 << 20
	// createdBudget separately bounds reported mktemp paths, so command
	// output cannot drop them.
	createdBudget = 1 << 20
	readSize      = 32 << 10
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == shellhook.Flag {
		os.Exit(trackingHook(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	}
	if len(os.Args) == 3 && os.Args[1] == "--created-path" {
		reportCreatedPath(os.Args[2])
		return
	}
	if len(os.Args) == 5 && os.Args[1] == "--sh-open" {
		if work := shOpen(os.Args[2], os.Args[3], os.Args[4]); work != "" {
			fmt.Println(work)
		}
		return
	}
	// The shell's process group may be signaled while its commands still
	// write; the relay ends with their output, not with the signal.
	signal.Ignore(syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGPIPE)
	os.Exit(run(os.Args[1:]))
}

// run declines by exiting before replying; the shell then runs the original
// script itself.
func run(args []string) int {
	var prepared, channel, directory string
	var count int
	terminal := term.IsTerminal(3)
	hello := segmenttransport.Message{Type: segmenttransport.Hello, Version: segmenttransport.Protocol, Thread: os.Getenv("CODEX_THREAD_ID"), Terminal: terminal}
	switch {
	case len(args) == 6 && args[0] == "--sh-run":
		prepared, channel, directory = args[3], args[1], args[2]
		work, ok := segmenttransport.ReportDirectory(directory, strings.TrimPrefix(filepath.Base(prepared), "run-"))
		if !ok || work != prepared {
			return 1
		}
		defer fmt.Println("decline") // Unblock the shell even when startup declines.
		var err error
		count, err = strconv.Atoi(args[5])
		if err != nil || count < 1 {
			return 1
		}
		hello.Digest, hello.Count = args[4], count
	case len(args) == 3:
		channel, directory = args[0], args[1]
		hello.Script = shellinstrumentation.ShOriginal(args[2])
		segments, ok := shellinstrumentation.Split(hello.Script)
		if !ok {
			return 0
		}
		count = len(segments)
		for _, segment := range segments {
			hello.Segments = append(hello.Segments, segment.Source)
		}
	default:
		return 2
	}
	var conn, answer *os.File
	var work string
	var err error
	if prepared != "" {
		conn, answer, work, err = segmenttransport.OpenPreparedReport(prepared, replyTimeout)
	} else {
		conn, answer, work, err = segmenttransport.OpenReport(channel, directory, replyTimeout)
	}
	if err != nil {
		return 0
	}
	defer conn.Close()
	defer answer.Close()
	if err := conn.SetWriteDeadline(time.Now().Add(replyTimeout)); err != nil {
		return 0
	}
	if err := writeMessage(conn, hello); err != nil {
		return 0
	}
	var reply segmenttransport.Reply
	if line, ok := readReply(int(answer.Fd()), replyTimeout); !ok || json.Unmarshal(line, &reply) != nil || !reply.OK {
		return 0
	}

	if err := conn.SetWriteDeadline(time.Time{}); err != nil {
		return 0
	}
	r := &relay{ack: 1, sinks: [2]int{3, 4}, readers: [2]int{-1, -1}, created: -1, report: newReporter(conn)}
	if prepared == "" {
		// Bash's coprocess pipe is unreachable from subshells and children
		// on Darwin, so its mktemp reports use the router-created control
		// FIFO. Holding both ends keeps late writers from blocking and the
		// idle FIFO from reporting hangup.
		fd, err := unix.Open(filepath.Join(work, "control"), unix.O_RDWR|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err != nil {
			return 0
		}
		defer unix.Close(fd)
		r.created = fd
	}
	mode := "status"
	if count == 1 {
		mode = "observe"
	} else if !terminal {
		for i, name := range []string{"out", "err"} {
			path := filepath.Join(work, name)
			// Opening without a writer must not block; the shell opens its end next.
			fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
			if err != nil {
				return 0
			}
			r.readers[i] = fd
		}
		mode = "relay"
	}
	if err := unix.SetNonblock(0, true); err != nil {
		return 0
	}
	if _, err := unix.Write(r.ack, []byte(mode+" "+work+"\n")); err != nil {
		return 0
	}
	r.run()
	r.readCreated()
	// FIFO EOF readiness can be missed on Darwin. This final record follows
	// every output record, including output after the shell's EXIT boundary.
	r.report.send(segmenttransport.Message{Type: segmenttransport.Closed})
	r.report.close(flushTimeout)
	return 0
}

// relay forwards the shell's output to its original descriptors and reports
// each segment's share. Output is read only after the shell opens its ends.
type relay struct {
	ack     int
	sinks   [2]int // Original stdout and stderr.
	readers [2]int // FIFO read ends, or -1.
	created int    // Bash's mktemp report FIFO, or -1.
	pending []byte // Its incomplete line.
	opened  bool
	control []byte
	current int
	started time.Time
	report  *reporter
}

func (r *relay) run() {
	r.current = -1
	controlOpen := true
	for controlOpen || r.opened && (r.readers[0] >= 0 || r.readers[1] >= 0) {
		var fds []unix.PollFd
		if controlOpen {
			fds = append(fds, unix.PollFd{Fd: 0, Events: unix.POLLIN})
		}
		if r.opened {
			for _, fd := range r.readers {
				if fd >= 0 {
					fds = append(fds, unix.PollFd{Fd: int32(fd), Events: unix.POLLIN})
				}
			}
		}
		if len(fds) == 0 {
			break
		}
		// Darwin can miss FIFO readiness while an external command blocks
		// in a large write. Bound that wait so the nonblocking drain can
		// read buffered output even without a readiness notification.
		timeout := -1
		if runtime.GOOS == "darwin" {
			timeout = 10
		}
		if _, err := unix.Poll(fds, timeout); err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			break
		}
		// The shell writes a segment's begin line before its output, so
		// boundaries come first; an end line drains the segment's output
		// before it is acknowledged.
		// The control pipe is nonblocking too: a missed EOF notification
		// must not keep the helper and Codex's output descriptors alive.
		if controlOpen && (fds[0].Revents != 0 || timeout >= 0) {
			controlOpen = r.readControl()
		}
		r.drain()
	}
	for i := range r.sinks {
		r.closeStream(i)
	}
}

// readControl handles boundary lines until the shell reports exit or closes
// its end of the control pipe.
func (r *relay) readControl() bool {
	buffer := make([]byte, 512)
	n, err := unix.Read(0, buffer)
	if err != nil && errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) {
		return true
	}
	if n <= 0 {
		return false
	}
	r.control = append(r.control, buffer[:n]...)
	for {
		line, rest, ok := strings.Cut(string(r.control), "\n")
		if !ok {
			return true
		}
		r.control = []byte(rest)
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if fields[0] != segmenttransport.Created {
			// Reports written before this boundary belong to the current segment.
			r.readCreated()
		}
		switch fields[0] {
		case segmenttransport.Created:
			r.sendCreated(line)
		case "o":
			r.opened = true
		case "b":
			if len(fields) == 2 {
				if index, err := strconv.Atoi(fields[1]); err == nil {
					r.current = index
					r.started = time.Now()
					r.report.send(segmenttransport.Message{Type: segmenttransport.Begin, Index: index, Timing: segmenttransport.Timing{Started: r.started}})
					// Separate control and output pipes can become readable in
					// either order. Let the shell produce output only after its
					// segment identity is installed, including the first segment.
					if _, err := unix.Write(r.ack, []byte("\n")); err != nil {
						return false
					}
				}
			}
		case "e", "d":
			ended := time.Now()
			r.drain()
			code := -1
			if len(fields) >= 2 {
				if value, err := strconv.Atoi(fields[len(fields)-1]); err == nil {
					code = value
				}
			}
			if fields[0] == "e" && len(fields) == 3 {
				if index, err := strconv.Atoi(fields[1]); err == nil {
					r.endSegment(index, code, ended)
				}
			} else if fields[0] == "d" {
				// exit/errexit can bypass the normal end hook. The EXIT
				// boundary ends the currently observed command, not the batch.
				r.endSegment(r.current, code, ended)
				r.report.send(segmenttransport.Message{Type: segmenttransport.Done, Code: new(code)})
			}
			// The shell waits for this before its next segment starts.
			if _, err := unix.Write(r.ack, []byte("\n")); err != nil {
				return false
			}
			// A detached child can inherit the control FIFO. EXIT is the
			// final boundary; only stdout and stderr still need draining.
			if fields[0] == "d" {
				return false
			}
		}
	}
}

func (r *relay) sendCreated(line string) {
	var message segmenttransport.Message
	if json.Unmarshal([]byte(strings.TrimPrefix(line, "c ")), &message) == nil && message.Type == segmenttransport.Created {
		message.Index = r.current
		r.report.send(message)
	}
}

// readCreated forwards the complete mktemp reports already in Bash's report
// FIFO. Their writers finished before the shell wrote its next boundary.
func (r *relay) readCreated() {
	if r.created < 0 {
		return
	}
	buffer := make([]byte, 512)
	for {
		n, err := unix.Read(r.created, buffer)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if n <= 0 {
			break
		}
		r.pending = append(r.pending, buffer[:n]...)
	}
	for {
		line, rest, ok := bytes.Cut(r.pending, []byte("\n"))
		if !ok {
			if len(r.pending) > 2*readSize {
				r.pending = nil // Not a report line.
			}
			return
		}
		r.pending = rest
		if text := string(line); strings.HasPrefix(text, segmenttransport.Created+" ") {
			r.sendCreated(text)
		}
	}
}

// reportCreatedPath reports a successful mktemp result that names an existing
// file or directory. Dry-run, help, and version output name nothing on disk.
func reportCreatedPath(output string) {
	path, ok := strings.CutSuffix(output, "\n")
	if !ok || path == "" || len(path) > readSize {
		return
	}
	if !filepath.IsAbs(path) {
		directory, err := os.Getwd()
		if err != nil {
			return
		}
		path = filepath.Join(directory, path)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() && !info.Mode().IsRegular() {
		return
	}
	message, err := json.Marshal(segmenttransport.Message{Type: segmenttransport.Created, Data: path, Tree: info.IsDir()})
	if err == nil {
		_, _ = unix.Write(3, append(append([]byte("c "), message...), '\n'))
	}
}

func (r *relay) endSegment(index, code int, ended time.Time) {
	if index != r.current || r.started.IsZero() {
		return
	}
	r.report.send(segmenttransport.Message{Type: segmenttransport.End, Index: index, Code: new(code), Timing: segmenttransport.Timing{Started: r.started, Ended: ended, ElapsedNS: int64(ended.Sub(r.started))}})
	r.started = time.Time{}
}

// drain relays whatever output is already buffered, without waiting.
func (r *relay) drain() {
	if !r.opened {
		return
	}
	buffer := make([]byte, readSize)
	for i, fd := range r.readers {
		for fd >= 0 {
			n, err := unix.Read(fd, buffer)
			if err != nil {
				if errors.Is(err, unix.EINTR) {
					continue
				}
				if !errors.Is(err, unix.EAGAIN) {
					r.closeStream(i)
				}
				break
			}
			if n == 0 {
				r.closeStream(i)
				break
			}
			if !writeAll(r.sinks[i], buffer[:n]) {
				// Codex stopped reading: the writers see the same broken pipe.
				r.closeStream(i)
				break
			}
			if r.current >= 0 {
				r.report.output(r.current, buffer[:n])
			}
		}
	}
}

func (r *relay) closeStream(i int) {
	if r.readers[i] >= 0 {
		unix.Close(r.readers[i])
		r.readers[i] = -1
	}
	if r.sinks[i] >= 0 && (!r.opened || r.readers[i] < 0) {
		// Codex sees the end of output once every writer has finished.
		unix.Close(r.sinks[i])
		r.sinks[i] = -1
	}
}

func writeAll(fd int, data []byte) bool {
	for len(data) > 0 {
		n, err := unix.Write(fd, data)
		if err != nil {
			if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) {
				if errors.Is(err, unix.EAGAIN) {
					unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}}, -1)
				}
				continue
			}
			return false
		}
		data = data[n:]
	}
	return true
}

// reporter sends messages without ever blocking the relay. When the router
// falls behind, output reports stop and the report ends incomplete, so the
// router falls back to the command's combined output.
type reporter struct {
	conn     *os.File
	messages chan segmenttransport.Message
	done     chan struct{}
	budget   int
	created  int // Remaining createdBudget.
	lossy    bool
	failed   bool
}

func newReporter(conn *os.File) *reporter {
	r := &reporter{conn: conn, messages: make(chan segmenttransport.Message, 1024), done: make(chan struct{}), budget: reportBudget, created: createdBudget}
	go func() {
		defer close(r.done)
		writer := bufio.NewWriter(conn)
		broken := false
		for message := range r.messages {
			if broken {
				continue
			}
			if writeMessage(writer, message) != nil {
				broken = true
				continue
			}
			if len(r.messages) == 0 && writer.Flush() != nil {
				broken = true
			}
		}
		if !broken {
			_ = writer.Flush()
		}
	}()
	return r
}

func (r *reporter) send(message segmenttransport.Message) {
	if r.failed {
		return
	}
	if message.Type == segmenttransport.Created {
		if r.created -= len(message.Data); r.created < 0 {
			return
		}
	}
	select {
	case r.messages <- message:
	default:
		r.failed = true
	}
}

func (r *reporter) output(index int, data []byte) {
	if r.lossy || r.failed {
		return
	}
	if r.budget -= len(data); r.budget < 0 {
		r.lossy = true
		r.send(segmenttransport.Message{Type: segmenttransport.Lossy})
		return
	}
	r.send(segmenttransport.Message{Type: segmenttransport.Output, Index: index, Data: strings.ToValidUTF8(string(data), "�")})
}

func (r *reporter) close(timeout time.Duration) {
	if r.failed {
		// An incomplete report must not end like a complete one.
		r.conn.Close()
	}
	close(r.messages)
	select {
	case <-r.done:
	case <-time.After(timeout):
	}
}

// readReply reads the router's one-line reply within timeout.
func readReply(fd int, timeout time.Duration) ([]byte, bool) {
	deadline := time.Now().Add(timeout)
	var line []byte
	buffer := make([]byte, 64)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, false
		}
		n, err := unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, int(remaining.Milliseconds())+1)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil || n == 0 {
			return nil, false
		}
		read, err := unix.Read(fd, buffer)
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) {
			continue // A nonblocking FIFO reader can poll ready spuriously.
		}
		if err != nil || read == 0 {
			return nil, false
		}
		line = append(line, buffer[:read]...)
		if reply, _, ok := bytes.Cut(line, []byte("\n")); ok {
			return reply, true
		}
	}
}

func writeMessage(w io.Writer, message segmenttransport.Message) error {
	line, err := json.Marshal(message)
	if err != nil {
		return err
	}
	_, err = w.Write(append(line, '\n'))
	return err
}
