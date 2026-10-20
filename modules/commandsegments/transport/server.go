package transport

import (
	"bufio"
	"context"
	json "encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yusing/mekugi/libraries/shellinstrumentation"
	"golang.org/x/sys/unix"
)

// Endpoints names existing invocation-private resources. Helpers only open them.
type Endpoints struct{ Requests, Directory string }

// Report receives one accepted stream. Abort withdraws a claim whose positive
// reply could not be delivered. Otherwise End follows all messages, even on loss.
// Implementations must return promptly; shutdown joins these callbacks.
type Report interface {
	Observe(Message)
	End()
	Abort()
}

type Claim func(context.Context, Message) Report

// Server owns transport only. A consumer supplies host correlation and policy.
type Server struct {
	endpoints Endpoints
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	mu        sync.Mutex
	claim     Claim
}

// Listen acquires a private transport root independent of frontend authentication.
// Source: internal/router/exec_track.go:98:169@[34c92f4c] FIFO listener and handshake
func Listen(ctx context.Context) (*Server, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := os.MkdirTemp("", "mekugi-segments-")
	if err != nil {
		return nil, err
	}
	endpoints := Endpoints{Requests: filepath.Join(root, "requests"), Directory: filepath.Join(root, "reports")}
	if err := os.Mkdir(endpoints.Directory, 0o700); err != nil {
		os.RemoveAll(root)
		return nil, err
	}
	if err := unix.Mkfifo(endpoints.Requests, 0o600); err != nil {
		os.RemoveAll(root)
		return nil, &os.PathError{Op: "mkfifo", Path: endpoints.Requests, Err: err}
	}
	// Holding the write end prevents idle EOF.
	requests, err := os.OpenFile(endpoints.Requests, os.O_RDWR, 0)
	if err != nil {
		os.RemoveAll(root)
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &Server{endpoints: endpoints, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	stop := context.AfterFunc(ctx, func() { requests.Close() })
	go func() {
		var reports sync.WaitGroup
		defer func() {
			cancel()
			requests.Close()
			stop()
			reports.Wait()
			os.RemoveAll(root)
			close(s.done)
		}()
		reader := bufio.NewReader(requests)
		for {
			line, err := reader.ReadSlice('\n')
			if errors.Is(err, bufio.ErrBufferFull) {
				for errors.Is(err, bufio.ErrBufferFull) {
					_, err = reader.ReadSlice('\n')
				}
				continue
			}
			if err != nil {
				return
			}
			work, ok := ReportDirectory(endpoints.Directory, strings.TrimSuffix(string(line), "\n"))
			if !ok {
				continue
			}
			s.mu.Lock()
			claim := s.claim
			s.mu.Unlock()
			if claim == nil || ctx.Err() != nil {
				continue
			}
			reports.Go(func() { acceptReport(ctx, work, claim) })
		}
	}()
	return s, nil
}

func (s *Server) Endpoints() Endpoints { return s.endpoints }

// Register installs the invocation's only consumer. It cannot be replaced.
func (s *Server) Register(claim Claim) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if s.claim != nil {
		return errors.New("segment report consumer already registered")
	}
	if claim == nil {
		return errors.New("missing segment report consumer")
	}
	s.claim = claim
	return nil
}

func (s *Server) Close() {
	if s == nil {
		return
	}
	s.cancel()
	<-s.done
}

// The server creates all command files outside Codex's sandbox. The helper can
// open these existing FIFOs even with a read-only mount.
func acceptReport(ctx context.Context, work string, claim Claim) {
	if err := os.Mkdir(work, 0o700); err != nil {
		return
	}
	defer os.RemoveAll(work)
	for _, name := range []string{"report", "reply", "out", "err", "control", "ack"} {
		if err := unix.Mkfifo(filepath.Join(work, name), 0o600); err != nil {
			return
		}
	}
	fd, err := unix.Open(filepath.Join(work, "report"), unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return
	}
	report := os.NewFile(uintptr(fd), filepath.Join(work, "report"))
	defer report.Close()
	// The helper opens report for writing before reply for reading.
	deadline := time.Now().Add(time.Second)
	for {
		fd, err = unix.Open(filepath.Join(work, "reply"), unix.O_WRONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.ENXIO) || time.Now().After(deadline) || ctx.Err() != nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	reply := os.NewFile(uintptr(fd), filepath.Join(work, "reply"))
	defer reply.Close()
	serve(ctx, report, reply, work, claim)
}

// Source: internal/router/exec_track.go:315:384@[34c92f4c] report validation and dispatch
func serve(ctx context.Context, report, reply *os.File, work string, claim Claim) {
	stop := context.AfterFunc(ctx, func() { report.Close(); reply.Close() })
	defer stop()
	reader := bufio.NewReaderSize(report, 64<<10)
	if err := report.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		return
	}
	var hello Message
	if line, err := reader.ReadBytes('\n'); err != nil || json.Unmarshal(line, &hello) != nil {
		return
	}
	if hello.Type != Hello || hello.Version != Protocol || len(hello.Segments) == 0 {
		return
	}
	segments, ok := shellinstrumentation.Split(hello.Script)
	if !ok || len(segments) != len(hello.Segments) {
		_ = writeReply(reply, false)
		return
	}
	for i, segment := range segments {
		if segment.Source != hello.Segments[i] {
			_ = writeReply(reply, false)
			return
		}
	}
	if len(segments) > 1 {
		if err := os.WriteFile(filepath.Join(work, "script"), []byte(shellinstrumentation.Rewrite(hello.Script, segments)), 0o600); err != nil {
			return
		}
	}
	stream := claim(ctx, hello)
	if err := writeReply(reply, stream != nil); err != nil || stream == nil {
		if stream != nil {
			stream.Abort()
		}
		return
	}
	defer stream.End()
	if err := report.SetReadDeadline(time.Time{}); err != nil {
		return
	}
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			var message Message
			if json.Unmarshal(line, &message) != nil {
				return
			}
			stream.Observe(message)
			if message.Type == Closed {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func writeReply(conn *os.File, ok bool) error {
	if err := conn.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		return err
	}
	line, err := json.Marshal(Reply{OK: ok})
	if err != nil {
		return err
	}
	_, err = conn.Write(append(line, '\n'))
	return err
}
