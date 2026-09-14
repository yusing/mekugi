package router

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"mvdan.cc/sh/v3/interp"
)

const outputReadUsage = "houtput ID [--stdout|--stderr] [--max-tokens N] [--cursor HASH:BYTE]"

type outputReadOptions struct {
	id        string
	stream    string
	cursor    string
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
		case "--max-tokens", "--cursor":
			if len(arguments) == 0 || arguments[0] == "" {
				return options, fmt.Errorf("%s requires a value", flag)
			}
			value := arguments[0]
			arguments = arguments[1:]
			if flag == "--cursor" {
				options.cursor = value
				continue
			}
			number, err := strconv.Atoi(value)
			if err != nil || number < 1 || number > hrunMaxTokens || strconv.Itoa(number) != value {
				return options, fmt.Errorf("--max-tokens requires an integer from 1 to %d", hrunMaxTokens)
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

func executeHOutput(ctx context.Context, manifest toolWorkerManifest, runtimeRoot string, arguments []string) error {
	handler := interp.HandlerCtx(ctx)
	fail := func(err error) error {
		_, _ = fmt.Fprintf(handler.Stderr, "houtput: %v\n", err)
		return interp.ExitStatus(1)
	}
	options, err := parseOutputRead(arguments)
	if err != nil {
		return fail(err)
	}
	store, err := shellOutputStore(manifest)
	if err != nil {
		return fail(err)
	}
	record, err := store.readShellOutput(ctx, options.id)
	if err != nil {
		return fail(err)
	}
	stdout, stderr := record.Stdout, record.Stderr
	if options.stream == "stdout" {
		stderr = ""
	} else if options.stream == "stderr" {
		stdout = ""
	}
	text := stdout + stderr
	// Bind both the stream boundary and the requested selection, including when
	// stdout and stderr happen to contain identical bytes.
	binding := options.id + ":" + options.stream + ":" + strconv.Itoa(len(stdout)) + ":" + text
	digest, offset, err := readCursorOffset(text, options.cursor, binding)
	if err != nil {
		return fail(err)
	}
	// Reserve a conservative byte/token upper bound for the two stream frames.
	const frameBudget = 64
	if options.maxTokens <= frameBudget {
		return fail(errors.New("token budget cannot admit stream frames; increase --max-tokens above 64"))
	}
	selected, err := selectReadPage(ctx, manifest, runtimeRoot, text[offset:], options.maxTokens-frameBudget)
	if err != nil {
		return fail(err)
	}
	next := offset + len(selected)
	var page strings.Builder
	if options.stream != "stderr" {
		page.WriteString("--- stdout ---\n")
		start, end := min(offset, len(stdout)), min(next, len(stdout))
		page.WriteString(stdout[start:end])
		page.WriteByte('\n')
	}
	if options.stream != "stdout" {
		page.WriteString("--- stderr ---\n")
		start, end := max(0, offset-len(stdout)), max(0, next-len(stdout))
		page.WriteString(stderr[start:end])
		page.WriteByte('\n')
	}
	if _, err := io.WriteString(handler.Stdout, page.String()); err != nil {
		return err
	}
	if next < len(text) {
		_, _ = fmt.Fprintf(handler.Stderr, "houtput: incomplete; repeat this read with --cursor %s:%d\n", digest, next)
		return interp.ExitStatus(1)
	}
	return nil
}
