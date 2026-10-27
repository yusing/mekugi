package recovery

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/libraries/commandargs"

	"github.com/yusing/mekugi/libraries/outputbudget/tokenizer"
	"github.com/yusing/mekugi/modules/outputrecovery/internal/paging"
	"github.com/yusing/mekugi/modules/outputrecovery/internal/record"
)

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
		arguments = commandargs.ExpandMaxTokensOption(arguments)
		flag := arguments[0]
		arguments = arguments[1:]
		if !strings.HasPrefix(flag, "--") {
			if !record.ValidID(flag) {
				return options, errors.New("REF must be a returned handle such as amber; paths and ranges belong to mcat")
			}
			options.ids = append(options.ids, flag)
			continue
		}
		if seen[flag] {
			if flag == "--max-tokens" {
				return options, errors.New(commandargs.MaxTokensArgumentError)
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
				return options, errors.New(commandargs.MaxTokensArgumentError)
			}
			value := arguments[0]
			arguments = arguments[1:]
			number, err := commandargs.ParseMaxTokens(value)
			if err != nil {
				return options, err
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

func (service *Service) Execute(ctx context.Context, request command.Request) (command.Output, error) {
	return service.execute(ctx, request), nil
}

func (service *Service) execute(ctx context.Context, request command.Request) command.Output {
	fail := func(err error) command.Output {
		return command.Output{Stderr: fmt.Sprintf("mread: %v\n", err), ExitCode: 1}
	}
	options, err := parseOutputRead(request.Args)
	if err != nil {
		return fail(err)
	}
	store, err := service.Attach(request.Scope, DefaultLimits(), nil)
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
	var diagnostics strings.Builder
	for _, id := range options.ids {
		err := func() error {
			cursor, err := store.read(ctx, id)
			if err != nil {
				return err
			}
			source := cursor
			binding := ""
			stream := options.stream
			if cursor.Source != "" {
				source, err = store.read(ctx, cursor.Source)
				if err != nil {
					return err
				}
				if source.Source != "" {
					return errors.New("read snapshot changed or is invalid")
				}
				binding = record.Binding(source)
				if binding != cursor.Binding {
					return errors.New("read snapshot changed or is invalid")
				}
				if stream != "" && stream != cursor.Stream {
					return errors.New("continuation already binds a stream; use the initial reference to change selection")
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
				return nil
			}
			output, err := store.streams(ctx, source)
			if err != nil {
				return err
			}
			request := paging.ReadRequest{Stdout: output.Stdout, Stderr: output.Stderr, StdoutKind: output.StdoutKind, StderrKind: output.StderrKind,
				Position: cursor.Position, Stream: stream, SourceRow: source.SourceRow}
			if len(options.ids) > 1 {
				request.Label = id
			}
			page, err := paging.Page(ctx, request, remaining)
			if err != nil {
				return err
			}
			if page.NeededTokens > 0 {
				if result.Len() != 0 {
					nextHandles = append(nextHandles, id)
					return nil
				}
				if page.NeededTokens > commandargs.MaxOutputTokens {
					return fmt.Errorf("next row needs ~%d tokens (maximum %d); use mrun with a byte-oriented command", page.NeededTokens, commandargs.MaxOutputTokens)
				}
				return fmt.Errorf("next row needs ~%d tokens; retry: mread %s --max-tokens %d", page.NeededTokens, id, page.NeededTokens)
			}
			pageTokens := used
			if page.Text != "" {
				tokens, err := codec.Count(result.String() + separator + page.Text)
				if err != nil {
					return err
				}
				if tokens > options.maxTokens {
					nextHandles = append(nextHandles, id)
					return nil
				}
				pageTokens = tokens
			}
			next := ""
			if !page.Complete {
				if binding == "" {
					binding = record.Binding(source)
				}
				// Persist before exposing this page or suggesting the next operation.
				next, err = store.putCursor(ctx, source, binding, page.Position, stream)
				if err != nil {
					return err
				}
			}
			if page.Text != "" {
				result.WriteString(separator)
				result.WriteString(page.Text)
				used = pageTokens
			}
			if next != "" {
				nextHandles = append(nextHandles, next)
			}
			return nil
		}()
		if err != nil {
			if len(options.ids) == 1 {
				fmt.Fprintf(&diagnostics, "mread: %v\n", err)
			} else {
				fmt.Fprintf(&diagnostics, "mread: %s: %v\n", id, err)
			}
			if ctx.Err() != nil {
				break
			}
		}
	}
	if len(nextHandles) != 0 {
		budget := 0
		if options.maxTokens != 8000 {
			budget = options.maxTokens
		}
		return command.Output{Stdout: result.String(), Stderr: diagnostics.String() + NextCall(strings.Join(nextHandles, " "), budget), ExitCode: 1}
	}
	status := 0
	if diagnostics.Len() > 0 {
		status = 1
	}
	return command.Output{Stdout: result.String(), Stderr: diagnostics.String(), ExitCode: status}
}
