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
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/yusing/mekugi/internal/execsegment"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

const (
	replyTimeout = 500 * time.Millisecond // Includes the router's wait for Codex's command item.
	flushTimeout = time.Second
	// reportBudget bounds the output copied to the router. Past it, output
	// still reaches Codex unchanged, but segment output is not reported.
	reportBudget = 4 << 20
	readSize     = 32 << 10
)

func main() {
	// The shell's process group may be signaled while its commands still
	// write; the relay ends with their output, not with the signal.
	signal.Ignore(syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGPIPE)
	os.Exit(run(os.Args[1:]))
}

// run declines by exiting before replying; the shell then runs the original
// script itself.
func run(args []string) int {
	if len(args) != 3 {
		return 2
	}
	channel, directory, script := args[0], args[1], args[2]
	segments, ok := execsegment.Split(script)
	if !ok {
		return 0
	}
	terminal := term.IsTerminal(3)
	sources := make([]string, len(segments))
	for i, segment := range segments {
		sources[i] = segment.Source
	}
	conn, answer, work, err := execsegment.OpenReport(channel, directory, replyTimeout)
	if err != nil {
		return 0
	}
	defer conn.Close()
	defer answer.Close()
	if err := conn.SetWriteDeadline(time.Now().Add(replyTimeout)); err != nil {
		return 0
	}
	hello := execsegment.Message{Type: execsegment.Hello, Version: execsegment.Protocol, Thread: os.Getenv("CODEX_THREAD_ID"), Script: script, Segments: sources, Terminal: terminal}
	if err := writeMessage(conn, hello); err != nil {
		return 0
	}
	var reply execsegment.Reply
	if line, ok := readReply(int(answer.Fd()), replyTimeout); !ok || json.Unmarshal(line, &reply) != nil || !reply.OK {
		return 0
	}

	if err := conn.SetWriteDeadline(time.Time{}); err != nil {
		return 0
	}
	r := &relay{ack: 1, sinks: [2]int{3, 4}, readers: [2]int{-1, -1}, report: newReporter(conn)}
	mode := "status"
	if !terminal {
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
	if _, err := unix.Write(r.ack, []byte(mode+" "+work+"\n")); err != nil {
		return 0
	}
	r.run()
	r.report.close(flushTimeout)
	return 0
}

// relay forwards the shell's output to its original descriptors and reports
// each segment's share. Output is read only after the shell opens its ends.
type relay struct {
	ack     int
	sinks   [2]int // Original stdout and stderr.
	readers [2]int // FIFO read ends, or -1.
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
		if _, err := unix.Poll(fds, -1); err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			break
		}
		// The shell writes a segment's begin line before its output, so
		// boundaries come first; an end line drains the segment's output
		// before it is acknowledged.
		if controlOpen && fds[0].Revents != 0 {
			controlOpen = r.readControl()
		}
		r.drain()
	}
	for i := range r.sinks {
		r.closeStream(i)
	}
}

// readControl handles the shell's complete boundary lines, and reports false
// once the shell closed its end, by exiting or replacing itself.
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
		switch fields[0] {
		case "o":
			r.opened = true
		case "b":
			if len(fields) == 2 {
				if index, err := strconv.Atoi(fields[1]); err == nil {
					r.current = index
					r.started = time.Now()
					r.report.send(execsegment.Message{Type: execsegment.Begin, Index: index, Timing: execsegment.Timing{Started: r.started}})
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
				r.report.send(execsegment.Message{Type: execsegment.Done, Code: new(code)})
			}
			// The shell waits for this before its next segment starts.
			if _, err := unix.Write(r.ack, []byte("\n")); err != nil {
				return false
			}
		}
	}
}

func (r *relay) endSegment(index, code int, ended time.Time) {
	if index != r.current || r.started.IsZero() {
		return
	}
	r.report.send(execsegment.Message{Type: execsegment.End, Index: index, Code: new(code), Timing: execsegment.Timing{Started: r.started, Ended: ended, ElapsedNS: int64(ended.Sub(r.started))}})
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
	messages chan execsegment.Message
	done     chan struct{}
	budget   int
	lossy    bool
	failed   bool
}

func newReporter(conn *os.File) *reporter {
	r := &reporter{conn: conn, messages: make(chan execsegment.Message, 1024), done: make(chan struct{}), budget: reportBudget}
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

func (r *reporter) send(message execsegment.Message) {
	if r.failed {
		return
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
		r.send(execsegment.Message{Type: execsegment.Lossy})
		return
	}
	r.send(execsegment.Message{Type: execsegment.Output, Index: index, Data: strings.ToValidUTF8(string(data), "�")})
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

type byteWriter interface{ Write([]byte) (int, error) }

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
		if err != nil || read == 0 {
			return nil, false
		}
		line = append(line, buffer[:read]...)
		if reply, _, ok := bytes.Cut(line, []byte("\n")); ok {
			return reply, true
		}
	}
}

func writeMessage(w byteWriter, message execsegment.Message) error {
	line, err := json.Marshal(message)
	if err != nil {
		return err
	}
	_, err = w.Write(append(line, '\n'))
	return err
}
