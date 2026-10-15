// Package persistence owns application-write accounting and atomic publication.
// It leaves flushing to the kernel; successful publication is not a power-loss guarantee.
package persistence

import (
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
)

// Counter is invocation-owned, never global or restored as a lifetime total.
type Counter struct{ bytes atomic.Uint64 }

// Snapshot describes instrumented application writes, not physical device I/O.
type Snapshot struct {
	Bytes uint64 `json:"bytes"`
	Scope string `json:"scope"`
}

func (c *Counter) Snapshot() *Snapshot {
	if c == nil {
		return nil
	}
	return &Snapshot{Bytes: c.bytes.Load(), Scope: "router-process managed records and debug/capture writes; excludes subprocesses, filesystem metadata and physical device I/O"}
}

// Writer counts the returned byte count even when the write also reports an error.
// Wrap the file, not a buffer: buffered bytes have not yet reached the kernel.
func (c *Counter) Writer(w io.Writer) io.Writer {
	if c == nil {
		return w
	}
	return countedWriter{w, c}
}

type countedWriter struct {
	writer  io.Writer
	counter *Counter
}

func (w countedWriter) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	if n > 0 {
		w.counter.bytes.Add(uint64(n))
	}
	return n, err
}

// AtomicFile closes a private same-directory temporary file before rename.
// A failed publication still accounts bytes successfully written to its temp file.
func AtomicFile(path, pattern string, data []byte, counter *Counter) error {
	f, err := os.CreateTemp(filepath.Dir(path), pattern)
	if err != nil {
		return err
	}
	defer func() { f.Close(); os.Remove(f.Name()) }()
	n, err := counter.Writer(f).Write(data)
	if err != nil {
		return err
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
