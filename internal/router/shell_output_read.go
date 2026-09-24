package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/yusing/mekugi/internal/router/toolplugin"
	"github.com/yusing/mekugi/internal/tokenizer"
)

const maxOutputTokens = 15_500

const outputReadUsage = "mread REF [REF ...] [--stdout|--stderr] [--max-tokens N]"

type outputReadOptions struct {
	ids       []string
	stream    string
	maxTokens int
}

func parseOutputRead(arguments []string) (outputReadOptions, error) {
	options := outputReadOptions{maxTokens: 8000}
	seen := make(map[string]bool)
	for len(arguments) > 0 {
		arguments = expandMaxTokensOption(arguments)
		flag := arguments[0]
		arguments = arguments[1:]
		if !strings.HasPrefix(flag, "--") {
			if !validShellOutputID(flag) {
				return options, errors.New("REF must be a returned handle such as amber; paths and ranges belong to mcat")
			}
			options.ids = append(options.ids, flag)
			continue
		}
		if seen[flag] {
			if flag == "--max-tokens" {
				return options, errors.New(maxTokensArgumentError)
			}
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
				return options, errors.New(maxTokensArgumentError)
			}
			value := arguments[0]
			arguments = arguments[1:]
			number, err := strconv.Atoi(value)
			if err != nil || number < 1 || number > maxOutputTokens || strconv.Itoa(number) != value {
				return options, errors.New(maxTokensArgumentError)
			}
			options.maxTokens = number
		default:
			return options, fmt.Errorf("unknown option %s; %s", flag, outputReadUsage)
		}
	}
	if len(options.ids) == 0 {
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
	codec, err := tokenizer.New()
	if err != nil {
		return fail(err)
	}
	var result strings.Builder
	var nextHandles []string
	used := 0
	for _, id := range options.ids {
		cursor, err := store.readShellOutput(ctx, id)
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
		remaining := options.maxTokens - used
		separator := ""
		if result.Len() > 0 {
			separator = "\n"
			remaining--
		}
		if remaining <= 0 {
			nextHandles = append(nextHandles, id)
			continue
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
			SourceRow  uint64 `json:"sourceRow,omitzero"`
			Label      string `json:"label,omitempty"`
		}{Stdout: output.Stdout, Stderr: output.Stderr, StdoutKind: output.StdoutKind, StderrKind: output.StderrKind,
			Position: cursor.Position, Stream: stream, SourceRow: source.SourceRow}
		if len(options.ids) > 1 {
			request.Label = id
		}
		data, err := json.Marshal(request)
		if err != nil {
			return fail(err)
		}
		formatted, err := toolplugin.FormatOutput(ctx, manifest.NodeExecutable, runtimeRoot,
			[]string{strconv.Itoa(remaining), "read", string(data), ""})
		if err != nil {
			return fail(err)
		}
		var page struct {
			Text         string `json:"text"`
			Position     [2]int `json:"position"`
			Complete     bool   `json:"complete"`
			NeededTokens int    `json:"neededTokens"`
		}
		if formatted.ExitCode != 0 {
			return fail(fmt.Errorf("page selection failed: %s", strings.TrimSpace(formatted.Stderr)))
		}
		if json.Unmarshal([]byte(formatted.Stdout), &page) != nil {
			return fail(errors.New("invalid read page"))
		}
		if page.NeededTokens > 0 {
			if result.Len() != 0 {
				nextHandles = append(nextHandles, id)
				continue
			}
			if page.NeededTokens > maxOutputTokens {
				return fail(fmt.Errorf("next row needs ~%d tokens (maximum %d); use mrun with a byte-oriented command", page.NeededTokens, maxOutputTokens))
			}
			return fail(fmt.Errorf("next row needs ~%d tokens; retry: mread %s --max-tokens %d", page.NeededTokens, id, page.NeededTokens))
		}
		if page.Text != "" {
			tokens, err := codec.Count(result.String() + separator + page.Text)
			if err != nil {
				return fail(err)
			}
			if tokens > options.maxTokens {
				nextHandles = append(nextHandles, id)
				continue
			}
			result.WriteString(separator)
			result.WriteString(page.Text)
			used = tokens
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
			nextHandles = append(nextHandles, next)
		}
	}
	if len(nextHandles) != 0 {
		budget := 0
		if options.maxTokens != 8000 {
			budget = options.maxTokens
		}
		return toolplugin.ExecutionOutput{Stdout: result.String(), Stderr: readNextCall(strings.Join(nextHandles, " "), budget), ExitCode: 1}
	}
	return toolplugin.ExecutionOutput{Stdout: result.String()}
}
