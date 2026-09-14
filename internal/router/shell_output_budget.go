package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"

	"github.com/yusing/mekugi/internal/router/toolplugin"
)

// shellOutputDisplay bounds what reaches Codex, not what commands execute or
// write to their own pipes/files. One balance covers both outer streams.
type shellOutputDisplay struct {
	mu        sync.Mutex
	ctx       context.Context
	manifest  toolWorkerManifest
	runtime   string
	remaining int
	omitted   bool
	streams   [2]shellDisplayStream
}

type shellDisplayStream struct {
	owner       *shellOutputDisplay
	destination io.Writer
	raw         bytes.Buffer
	shown       bytes.Buffer
}

func newShellOutputDisplay(ctx context.Context, manifest toolWorkerManifest, runtime string, tokens int, stdout, stderr io.Writer) *shellOutputDisplay {
	// Leave room for host metadata, continuation/retention notices, and framing.
	reserve := min(1024, tokens)
	display := &shellOutputDisplay{ctx: ctx, manifest: manifest, runtime: runtime, remaining: max(0, min(15500, tokens-reserve))}
	display.streams[0] = shellDisplayStream{owner: display, destination: stdout}
	display.streams[1] = shellDisplayStream{owner: display, destination: stderr}
	return display
}

func (stream *shellDisplayStream) Write(value []byte) (int, error) {
	display := stream.owner
	display.mu.Lock()
	defer display.mu.Unlock()
	_, _ = stream.raw.Write(value)
	text := string(value)
	tokens := 0
	if text != "" && display.remaining > 0 {
		// Escaped bytes are a conservative token upper bound. Small progress
		// writes need no tokenizer subprocess.
		encoded, _ := json.Marshal(text)
		framed, _ := json.Marshal(string(encoded))
		tokens = len(framed)
		if tokens > display.remaining {
			result, err := toolplugin.FormatOutput(display.ctx, display.manifest.NodeExecutable, display.runtime,
				[]string{strconv.Itoa(display.remaining), "shell", text, ""})
			if err != nil {
				return 0, err
			}
			var selected struct {
				Text   string `json:"text"`
				Tokens int    `json:"tokens"`
			}
			if result.ExitCode != 0 || json.Unmarshal([]byte(result.Stdout), &selected) != nil ||
				!strings.HasPrefix(text, selected.Text) || selected.Tokens < 0 || selected.Tokens > display.remaining {
				return 0, errors.New("invalid shell output selection")
			}
			text, tokens = selected.Text, selected.Tokens
		}
	} else if text != "" {
		text = ""
	}
	if len(text) != len(value) {
		display.omitted = true
		// Once a stream has a gap, retain its suffix rather than interleave
		// later rows as if the displayed output were a complete prefix.
		display.remaining = 0
	} else {
		display.remaining -= tokens
	}
	_, _ = stream.shown.WriteString(text)
	if stream.destination != nil {
		n, err := io.WriteString(stream.destination, text)
		if err == nil && n != len(text) {
			err = io.ErrShortWrite
		}
		if err != nil {
			return 0, err
		}
	}
	return len(value), nil
}

func (display *shellOutputDisplay) finish(execution toolplugin.ExecutionOutput) (toolplugin.ExecutionOutput, error) {
	for index, text := range []string{execution.Stdout, execution.Stderr} {
		if _, err := io.WriteString(&display.streams[index], text); err != nil {
			return toolplugin.ExecutionOutput{}, err
		}
	}
	execution.Stdout, execution.Stderr = "", ""
	for index, target := range []*string{&execution.Stdout, &execution.Stderr} {
		if display.streams[index].destination == nil {
			*target = display.streams[index].shown.String()
		}
	}
	if !display.omitted {
		return execution, nil
	}
	retained, err := toolplugin.FormatOutput(display.ctx, display.manifest.NodeExecutable, display.runtime,
		[]string{"0", "retain", display.streams[0].raw.String(), display.streams[1].raw.String(),
			strconv.Itoa(display.streams[0].shown.Len()), strconv.Itoa(display.streams[1].shown.Len()), strconv.Itoa(execution.ExitCode)})
	if err != nil {
		return toolplugin.ExecutionOutput{}, fmt.Errorf("retain shell output: %w", err)
	}
	var paths struct {
		Directory string `json:"directory"`
	}
	if retained.ExitCode != 0 || json.Unmarshal([]byte(retained.Stdout), &paths) != nil || paths.Directory == "" {
		return toolplugin.ExecutionOutput{}, errors.New("invalid retained shell output")
	}
	execution.Stderr += fmt.Sprintf("\noutput: %s\n", paths.Directory)
	return execution, nil
}
