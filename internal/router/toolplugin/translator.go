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

// Translator keeps a router-owned declaration warm, without running executors.
// Calls are serialized because the shared WASM reactor owns mutable input memory.
// Configured plugins use Translate instead, preserving their per-call isolation.
type Translator struct {
	node, root, module string
	ctx                context.Context
	cancel             context.CancelFunc
	gate               chan struct{}
	process            *translationProcess // protected by gate
}

func NewTranslator(ctx context.Context, node, root, module string) (*Translator, error) {
	lifetime, cancel := context.WithCancel(context.Background())
	t := &Translator{node: node, root: root, module: module, ctx: lifetime, cancel: cancel, gate: make(chan struct{}, 1)}
	ctx, stop := context.WithTimeout(ctx, pluginInvocationTimeout)
	defer stop()
	if err := t.start(ctx); err != nil {
		cancel()
		return nil, fmt.Errorf("prepare plugin translator: %w", err)
	}
	return t, nil
}

func (t *Translator) Translate(ctx context.Context, index int, input string) (Translation, error) {
	ctx, cancel := context.WithTimeout(ctx, pluginInvocationTimeout)
	defer cancel()
	select {
	case t.gate <- struct{}{}:
		defer func() { <-t.gate }()
	case <-ctx.Done():
		return Translation{}, ctx.Err()
	case <-t.ctx.Done():
		return Translation{}, t.ctx.Err()
	}
	if err := t.ctx.Err(); err != nil {
		return Translation{}, err
	}
	if err := ctx.Err(); err != nil {
		return Translation{}, err
	}
	// An idle host may have exited since its last successful response. Replace
	// it before sending a new call, but never retry a call already sent.
	if t.process != nil {
		select {
		case <-t.process.done:
			t.process.stop()
			t.process = nil
		default:
		}
	}
	if t.process == nil {
		if err := t.start(ctx); err != nil {
			return Translation{}, err
		}
	}
	request := struct {
		Index int    `json:"index"`
		Input string `json:"input"`
	}{index, input}
	var result *Translation
	err := t.process.exchange(ctx, request, &result)
	if err == nil && (result == nil || result.Arguments == nil || (!result.Rejected && result.Carrier.Kind == "")) {
		err = errors.New("plugin translator returned an incomplete result")
	}
	if err != nil {
		t.process.stop()
		t.process = nil
		if errors.Is(err, context.DeadlineExceeded) {
			return Translation{}, fmt.Errorf("plugin translation exceeded %s: %w", pluginInvocationTimeout, err)
		}
		return Translation{}, err
	}
	return *result, nil
}

func (t *Translator) Close() {
	if t == nil {
		return
	}
	t.cancel()
	t.gate <- struct{}{}
	defer func() { <-t.gate }()
	if t.process != nil {
		t.process.stop()
		t.process = nil
	}
}

func (t *Translator) start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	lifetime, cancel := context.WithCancel(t.ctx)
	command := exec.CommandContext(lifetime, t.node, filepath.Join(t.root, hostFilename), "--translate-server")
	ConfigureProcessGroup(command)
	command.WaitDelay = time.Second
	command.Dir = filepath.Join(t.root, snapshotDirectory)
	command.Env = []string{"HOME=" + t.root, "NODE_NO_WARNINGS=1", "PATH=" + filepath.Dir(t.node)}
	stdin, err := command.StdinPipe()
	if err != nil {
		cancel()
		return err
	}
	// Own the read descriptor so Wait cannot close it before a complete response
	// is consumed, and cancellation can unblock reads even if a child holds it.
	stdout, writer, err := os.Pipe()
	if err != nil {
		cancel()
		_ = stdin.Close()
		return err
	}
	p := &translationProcess{ctx: lifetime, cancel: cancel, stdin: stdin, stdout: stdout, done: make(chan struct{})}
	command.Stdout = writer
	command.Stderr = &p.diagnostics
	p.scanner = bufio.NewScanner(stdout)
	p.scanner.Buffer(make([]byte, 4096), ExecutionOutputBudgetBytes+1)
	if err := command.Start(); err != nil {
		cancel()
		_ = stdin.Close()
		_ = stdout.Close()
		_ = writer.Close()
		return err
	}
	_ = writer.Close()
	go func() {
		p.waitErr = command.Wait()
		close(p.done)
	}()
	request := struct {
		SnapshotRoot string `json:"snapshotRoot"`
		Module       string `json:"module"`
	}{command.Dir, t.module}
	var ready struct {
		Ready bool `json:"ready"`
	}
	if err := p.exchange(ctx, request, &ready); err != nil {
		p.stop()
		return err
	}
	if !ready.Ready {
		p.stop()
		return errors.New("plugin translator did not become ready")
	}
	t.process = p
	return nil
}

type translationProcess struct {
	ctx         context.Context
	cancel      context.CancelFunc
	stdin       io.WriteCloser
	stdout      *os.File
	scanner     *bufio.Scanner
	diagnostics translationDiagnostics
	done        chan struct{}
	waitErr     error // read only after done
}

func (p *translationProcess) stop() {
	p.cancel()
	_ = p.stdin.Close()
	_ = p.stdout.Close()
	<-p.done
}

func (p *translationProcess) exchange(ctx context.Context, request, response any) error {
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
		// Closing the host can make the pipe result win the select. Preserve
		// caller/lifetime cancellation instead of reporting an incidental EOF.
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
			return fmt.Errorf("plugin translator failed: %w: %v: %s", reply.err, p.waitErr, p.diagnostics.String())
		}
		decoder := json.NewDecoder(bytes.NewReader(reply.data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(response); err != nil {
			return fmt.Errorf("decode plugin translation: %w", err)
		}
		if decoder.Decode(&struct{}{}) != io.EOF {
			return errors.New("plugin translator returned trailing output")
		}
		return nil
	}
}

// Persistent hosts must not accumulate unbounded diagnostics between calls.
type translationDiagnostics struct {
	mu   sync.Mutex
	data []byte
}

func (d *translationDiagnostics) Write(data []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	const limit = 16 << 10
	d.data = append(d.data, data[:min(len(data), limit-len(d.data))]...)
	return len(data), nil
}

func (d *translationDiagnostics) String() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return string(d.data)
}
