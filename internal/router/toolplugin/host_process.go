package toolplugin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

func startHost(ctx, owner context.Context, node, host, mode, directory string, responseLimit int) (*hostProcess, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lifetime, cancel := context.WithCancel(owner)
	command := exec.CommandContext(lifetime, node, host, mode)
	command.WaitDelay = time.Second
	command.Dir = directory
	command.Env = []string{"HOME=" + filepath.Dir(directory), "NODE_NO_WARNINGS=1", "PATH=" + filepath.Dir(node)}
	stdin, err := command.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, writer, err := os.Pipe()
	if err != nil {
		cancel()
		_ = stdin.Close()
		return nil, err
	}
	process := &hostProcess{ctx: lifetime, cancel: cancel, stdin: stdin, stdout: stdout, done: make(chan struct{})}
	command.Stdout = writer
	command.Stderr = &process.diagnostics
	process.scanner = bufio.NewScanner(stdout)
	process.scanner.Buffer(make([]byte, 4096), responseLimit)
	if err := command.Start(); err != nil {
		cancel()
		_ = stdin.Close()
		_ = stdout.Close()
		_ = writer.Close()
		return nil, err
	}
	_ = writer.Close()
	go func() {
		process.waitErr = command.Wait()
		close(process.done)
	}()
	return process, nil
}

type hostProcess struct {
	ctx         context.Context
	cancel      context.CancelFunc
	stdin       io.WriteCloser
	stdout      *os.File
	scanner     *bufio.Scanner
	diagnostics hostDiagnostics
	done        chan struct{}
	waitErr     error
}

func (p *hostProcess) stop() {
	p.cancel()
	_ = p.stdin.Close()
	_ = p.stdout.Close()
	<-p.done
}

func (p *hostProcess) exchange(ctx context.Context, request, response any) error {
	encoded, err := json.Marshal(request)
	if err != nil {
		return err
	}
	type reply struct {
		data []byte
		err  error
	}
	result := make(chan reply, 1)
	go func() {
		if _, err := p.stdin.Write(append(encoded, '\n')); err != nil {
			result <- reply{err: err}
			return
		}
		if !p.scanner.Scan() {
			err := p.scanner.Err()
			if err == nil {
				err = io.ErrUnexpectedEOF
			}
			result <- reply{err: err}
			return
		}
		result <- reply{data: bytes.Clone(p.scanner.Bytes())}
	}()
	select {
	case <-ctx.Done():
		p.stop()
		<-result
		return ctx.Err()
	case <-p.ctx.Done():
		p.stop()
		<-result
		return p.ctx.Err()
	case reply := <-result:
		if err := ctx.Err(); err != nil {
			p.stop()
			return err
		}
		if err := p.ctx.Err(); err != nil {
			p.stop()
			return err
		}
		if reply.err != nil {
			p.stop()
			return fmt.Errorf("plugin host failed: %w: %v: %s", reply.err, p.waitErr, p.diagnostics.String())
		}
		decoder := json.NewDecoder(bytes.NewReader(reply.data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(response); err != nil {
			return fmt.Errorf("decode plugin host response: %w", err)
		}
		if decoder.Decode(&struct{}{}) != io.EOF {
			return errors.New("plugin host returned trailing output")
		}
		return nil
	}
}

type hostDiagnostics struct {
	mu   sync.Mutex
	data []byte
}

func (d *hostDiagnostics) Write(data []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	const limit = 16 << 10
	d.data = append(d.data, data[:min(len(data), limit-len(d.data))]...)
	return len(data), nil
}

func (d *hostDiagnostics) String() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return string(d.data)
}
