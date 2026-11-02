package transport

import (
	"bufio"
	"context"
	json "encoding/json/v2"
	"errors"
	"io"
	"path/filepath"
	"time"
)

// Boundary is a synchronous native pre-tool observation, never an approval.
// Deadline covers transport and acquisition from native helper entry.
type Boundary struct {
	Thread, Turn, Item, Tool, CWD, Input string
	Deadline                             time.Time
}

const BoundaryHold = 500 * time.Millisecond

// ObserveBoundary waits only within the original deadline. Failure is auxiliary.
func ObserveBoundary(root string, request Boundary) {
	if !filepath.IsAbs(root) || !time.Now().Before(request.Deadline) {
		return
	}
	report, reply, _, err := OpenReport(filepath.Join(root, "requests"), filepath.Join(root, "reports"), time.Until(request.Deadline))
	if err != nil {
		return
	}
	defer report.Close()
	defer reply.Close()
	_ = report.SetWriteDeadline(request.Deadline)
	_ = reply.SetReadDeadline(request.Deadline)
	data, err := json.Marshal(struct {
		Type     string `json:"t"`
		Boundary Boundary
	}{"boundary", request})
	if err != nil || len(data) > 4<<20 {
		return
	}
	if _, err := report.Write(append(data, '\n')); err != nil {
		return
	}
	reader := bufio.NewReader(io.LimitReader(reply, 128))
	for time.Now().Before(request.Deadline) {
		_, err := reader.ReadBytes('\n')
		if !errors.Is(err, io.EOF) {
			return
		}
		// A new nonblocking FIFO reports EOF until its writer opens.
		time.Sleep(time.Millisecond)
	}
}

// RegisterBoundary installs the invocation's capture consumer independently of reports.
func (s *Server) RegisterBoundary(observe func(context.Context, Boundary)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.boundary != nil {
		return errBoundaryRegistered
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}
	s.boundary = observe
	return nil
}
