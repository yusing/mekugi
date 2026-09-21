package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/yusing/mekugi/internal/router/toolplugin"
)

const maxOutputTokens = 15_500

const outputReadUsage = "mread REF [--stdout|--stderr] [--max-tokens N]"

type outputReadOptions struct {
	id        string
	stream    string
	maxTokens int
}

func parseOutputRead(arguments []string) (outputReadOptions, error) {
	options := outputReadOptions{maxTokens: 4000}
	seen := make(map[string]bool)
	for len(arguments) > 0 {
		flag := arguments[0]
		arguments = arguments[1:]
		if !strings.HasPrefix(flag, "--") {
			if options.id != "" || !validShellOutputID(flag) {
				return options, errors.New(outputReadUsage)
			}
			options.id = flag
			continue
		}
		if seen[flag] {
			return options, fmt.Errorf("duplicate option %s", flag)
		}
		seen[flag] = true
		switch flag {
		case "--stdout", "--stderr":
			if options.stream != "" {
				return options, errors.New("choose either --stdout or --stderr")
			}
			options.stream = strings.TrimPrefix(flag, "--")
		case "--max-tokens":
			if len(arguments) == 0 {
				return options, errors.New("--max-tokens requires a value")
			}
			value := arguments[0]
			arguments = arguments[1:]
			number, err := strconv.Atoi(value)
			if err != nil || number < 1 || number > maxOutputTokens || strconv.Itoa(number) != value {
				return options, fmt.Errorf("--max-tokens requires an integer from 1 to %d", maxOutputTokens)
			}
			options.maxTokens = number
		default:
			return options, fmt.Errorf("unknown option %s; %s", flag, outputReadUsage)
		}
	}
	if options.id == "" {
		return options, errors.New(outputReadUsage)
	}
	return options, nil
}

func executeMRead(
	ctx context.Context,
	manifest toolWorkerManifest,
	runtimeRoot string,
	arguments []string,
) toolplugin.ExecutionOutput {
	fail := func(err error) toolplugin.ExecutionOutput {
		return toolplugin.ExecutionOutput{Stderr: fmt.Sprintf("mread: %v\n", err), ExitCode: 1}
	}
	options, err := parseOutputRead(arguments)
	if err != nil {
		return fail(err)
	}
	store, err := shellOutputStore(manifest)
	if err != nil {
		return fail(err)
	}
	cursor, err := store.readShellOutput(ctx, options.id)
	if err != nil {
		return fail(err)
	}
	source := cursor
	stream := options.stream
	if cursor.Source != "" {
		source, err = store.readShellOutput(ctx, cursor.Source)
		if err != nil {
			return fail(err)
		}
		if source.Source != "" || readRecordBinding(source) != cursor.Binding {
			return fail(errors.New("read snapshot changed or is invalid"))
		}
		if stream != "" && stream != cursor.Stream {
			return fail(errors.New("continuation already binds a stream; use the initial reference to change selection"))
		}
		stream = cursor.Stream
	}
	output, err := store.readSourceStreams(ctx, source)
	if err != nil {
		return fail(err)
	}
	request := struct {
		Stdout     string `json:"stdout"`
		Stderr     string `json:"stderr"`
		StdoutKind string `json:"stdoutKind"`
		StderrKind string `json:"stderrKind"`
		Position   [2]int `json:"position"`
		Stream     string `json:"stream"`
	}{output.Stdout, output.Stderr, output.StdoutKind, output.StderrKind, cursor.Position, stream}
	data, err := json.Marshal(request)
	if err != nil {
		return fail(err)
	}
	formatted, err := toolplugin.FormatOutput(ctx, manifest.NodeExecutable, runtimeRoot,
		[]string{strconv.Itoa(options.maxTokens), "read", string(data), ""})
	if err != nil {
		return fail(err)
	}
	var page struct {
		Text     string `json:"text"`
		Position [2]int `json:"position"`
		Complete bool   `json:"complete"`
	}
	if formatted.ExitCode != 0 {
		return fail(fmt.Errorf("page selection failed: %s", strings.TrimSpace(formatted.Stderr)))
	}
	if json.Unmarshal([]byte(formatted.Stdout), &page) != nil {
		return fail(errors.New("invalid read page"))
	}
	next := ""
	if !page.Complete {
		// Persist before exposing this page or suggesting the next operation.
		next, err = store.putReadCursor(ctx, source, page.Position, stream)
		if err != nil {
			return fail(err)
		}
	}
	if next != "" {
		return toolplugin.ExecutionOutput{Stdout: page.Text, Stderr: readNextCall(next), ExitCode: 1}
	}
	return toolplugin.ExecutionOutput{Stdout: page.Text}
}
