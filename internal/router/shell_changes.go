package router

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/yusing/mekugi/internal/hpatchsyntax"
	"github.com/yusing/mekugi/internal/router/toolplugin"
	"mvdan.cc/sh/v3/interp"
)

const changesReadUsage = "hchanges read ID[..ID] ... [--summary|--history] [--path PATH] [--workspace DIR] [--max-tokens N] [--cursor HASH:BYTE] (flags may appear anywhere)"
const maxChangeReadBytes = 64 << 20

type changeReadOptions struct {
	view      string
	path      string
	workspace string
	cursor    string
	maxTokens int
	ids       []string
}

func parseChangeRead(arguments []string, cwd string) (changeReadOptions, error) {
	options := changeReadOptions{workspace: cwd, maxTokens: 4000}
	if len(arguments) == 0 || arguments[0] != "read" {
		return options, errors.New(changesReadUsage)
	}
	arguments = arguments[1:]
	seen := make(map[string]bool)
	var refs []string
	for len(arguments) > 0 {
		flag := arguments[0]
		arguments = arguments[1:]
		if !strings.HasPrefix(flag, "--") {
			refs = append(refs, flag)
			continue
		}
		if seen[flag] {
			return options, fmt.Errorf("duplicate option %s", flag)
		}
		seen[flag] = true
		switch flag {
		case "--summary", "--history":
			if options.view != "" {
				return options, errors.New("choose either --summary or --history")
			}
			options.view = strings.TrimPrefix(flag, "--")
		case "--path", "--workspace", "--max-tokens", "--cursor":
			if len(arguments) == 0 {
				return options, fmt.Errorf("%s requires a value", flag)
			}
			value := arguments[0]
			arguments = arguments[1:]
			switch flag {
			case "--path":
				if value == "" {
					return options, errors.New("--path requires a nonempty recorded path")
				}
				options.path = value
			case "--workspace":
				options.workspace = value
			case "--cursor":
				if value == "" {
					return options, errors.New("--cursor requires a nonempty HASH:BYTE value")
				}
				options.cursor = value
			case "--max-tokens":
				number, err := strconv.Atoi(value)
				if err != nil || number < 1 || number > hrunMaxTokens || strconv.Itoa(number) != value {
					return options, fmt.Errorf("--max-tokens requires an integer from 1 to %d", hrunMaxTokens)
				}
				options.maxTokens = number
			}
		default:
			return options, fmt.Errorf("unknown option %s; %s", flag, changesReadUsage)
		}
	}
	if len(refs) == 0 {
		return options, errors.New(changesReadUsage)
	}
	var err error
	options.ids, err = expandChangeRefs(refs)
	if err != nil {
		return options, err
	}
	if options.workspace != "" {
		if !filepath.IsAbs(options.workspace) {
			options.workspace = filepath.Join(cwd, options.workspace)
		}
		options.workspace, err = filepath.EvalSymlinks(options.workspace)
		if err != nil {
			return options, fmt.Errorf("resolve workspace: %w", err)
		}
	}
	return options, nil
}

func trackedStatus(history mekugiHistory, confirmed bool) string {
	if history.translationError == "" && history.toolName == mekugiToolName {
		// Carrier kind identifies transport, not whether this retained script
		// hands off a mixed execution plan. Use the same framing owner as translation.
		_, mixed, _ := hpatchsyntax.SplitShell(history.script)
		if mixed || strings.HasPrefix(strings.TrimSpace(history.script), "resume ") {
			return "execution plan (see segment attempts)"
		}
	}
	switch {
	case history.translationError != "":
		return "rejected"
	case history.alreadySatisfied:
		return "no-op"
	case history.applied || confirmed:
		return "applied"
	default:
		return "prepared (application unconfirmed)"
	}
}

func (s *mekugiReplayStore) readChanges(ctx context.Context, options changeReadOptions) (string, error) {
	var index changeIndex
	err := s.readLocked(ctx, func() error {
		var err error
		index, err = s.readChangeIndex(options.workspace)
		return err
	})
	if err != nil {
		return "", err
	}
	// Membership and receipts are fixed in this snapshot. Replay translation
	// facts are immutable, so record reads and rendering need no store lock.
	return s.renderChanges(ctx, options, index)
}

func (s *mekugiReplayStore) renderChanges(ctx context.Context, options changeReadOptions, index changeIndex) (string, error) {
	var output strings.Builder
	matched := false
	for _, id := range options.ids {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		change, exists := index.Changes[id]
		if !exists {
			return "", fmt.Errorf("change %s is missing in workspace %q; check --workspace or explicit store cleanup", id, options.workspace)
		}
		if len(change.Calls) > 1 {
			fmt.Fprintf(&output, "%s attempts=%d\n", id, len(change.Calls))
		}
		if len(change.Calls) == 0 {
			fmt.Fprintf(&output, "%s pending (no completed result)\n", id)
		}
		for position, call := range change.Calls {
			record, found, err := s.read(options.workspace, call.ID, false)
			if err != nil {
				return "", err
			}
			if !found || record.History.ChangeID != id || record.History.CorrelationID != change.Correlation {
				return "", fmt.Errorf("change %s has a missing or inconsistent attempt", id)
			}
			history := record.History.history()
			if len(change.Calls) == 1 {
				fmt.Fprintf(&output, "%s %s\n", id, trackedStatus(history, call.Confirmed))
			} else {
				fmt.Fprintf(&output, "attempt %d %s\n", position+1, trackedStatus(history, call.Confirmed))
			}
			retained := strings.HasPrefix(strings.TrimLeft(history.recoveryBaseline(), "\r\n"), "in "+shellArtifactPrefix)
			if retained {
				output.WriteString("scope: retained shell script, not workspace files\n")
			}
			if options.view == "history" {
				fmt.Fprintf(&output, "%s input:\n%s\n", history.toolName, history.script)
				if history.evaluated != "" {
					fmt.Fprintf(&output, "evaluated script:\n%s\n", history.evaluated)
				}
				if history.report != "" {
					output.WriteString(strings.TrimPrefix(history.report, changeNotice(id)))
					output.WriteByte('\n')
				}
				if history.translationError != "" {
					output.WriteString(strings.TrimPrefix(history.translationError, changeNotice(id)))
					output.WriteByte('\n')
				}
			}
			for _, file := range history.reviewFiles {
				if options.path != "" && !changePathMatches(options, file.BeforePath, retained) && !changePathMatches(options, file.AfterPath, retained) {
					continue
				}
				matched = true
				if options.view == "summary" {
					output.WriteString(file.Summary())
				} else {
					output.WriteString(file.UnifiedDiff())
				}
			}
			if output.Len() > maxChangeReadBytes {
				return "", errors.New("change read exceeds 64 MiB; narrow the range, view, or --path")
			}
		}
	}
	if options.path != "" && !matched {
		fmt.Fprintf(&output, "no files match --path %q\n", options.path)
	}
	return output.String(), nil
}

// Match lexical workspace-relative and absolute spellings without consulting
// current files: historical paths may have been moved or deleted since capture.
func changePathMatches(options changeReadOptions, recorded string, retained bool) bool {
	if recorded == "" {
		return false
	}
	if options.path == recorded {
		return true
	}
	if retained || options.workspace == "" {
		return false
	}
	resolve := func(path string) string {
		if !filepath.IsAbs(path) {
			path = filepath.Join(options.workspace, path)
		}
		return filepath.Clean(path)
	}
	return resolve(options.path) == resolve(recorded)
}

// The cursor binds a byte offset to the complete selected projection. A recovery
// or new application receipt invalidates it rather than mixing two snapshots.
func changeReadOffset(text, cursor string) (string, int, error) {
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
	if cursor == "" {
		return digest, 0, nil
	}
	hash, number, ok := strings.Cut(cursor, ":")
	offset, err := strconv.Atoi(number)
	if !ok || hash != digest || err != nil || offset < 0 || offset >= len(text) ||
		strconv.Itoa(offset) != number || !utf8.RuneStart(text[offset]) {
		return "", 0, errors.New("invalid or stale change cursor; restart this read")
	}
	return digest, offset, nil
}

func executeHChanges(ctx context.Context, manifest toolWorkerManifest, runtimeRoot string, shellContribution *toolContribution, arguments []string) error {
	handler := interp.HandlerCtx(ctx)
	fail := func(err error) error {
		_, _ = fmt.Fprintf(handler.Stderr, "hchanges: %v\n", err)
		return interp.ExitStatus(1)
	}
	options, err := parseChangeRead(arguments, handler.Dir)
	if err != nil {
		return fail(err)
	}
	// The authenticated manifest pins the router's store. Do not derive it from
	// mutable child environment or create a store as a side effect of reading.
	if manifest.ReplayDirectory == "" {
		return fail(errors.New("change storage is unavailable"))
	}
	if info, err := os.Lstat(filepath.Join(manifest.ReplayDirectory, "store.lock")); err != nil || !info.Mode().IsRegular() {
		return fail(errors.New("change storage is missing or invalid"))
	}
	store := &mekugiReplayStore{directory: manifest.ReplayDirectory}
	text, err := store.readChanges(ctx, options)
	if err != nil {
		return fail(err)
	}
	digest, offset, err := changeReadOffset(text, options.cursor)
	if err != nil {
		return fail(err)
	}
	// Bound tokenizer input using its maximum 128-byte token size, exactly as
	// hrun does. Selection reuses the bundled GPT-5 tokenizer, not a second codec.
	end := min(len(text), offset+options.maxTokens*128+utf8.UTFMax)
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	formatted, err := toolplugin.Execute(ctx, manifest.NodeExecutable, runtimeRoot,
		shellContribution.Module, shellContribution.ModuleIndex,
		[]string{"--hrun-output", strconv.Itoa(options.maxTokens), "head", text[offset:end], ""},
		nil, handler.Dir, shellEnvironment(handler.Env))
	if err != nil {
		return fail(err)
	}
	if formatted.ExitCode != 0 || !strings.HasPrefix(text[offset:end], formatted.Stdout) {
		return fail(errors.New("change output selection failed"))
	}
	if formatted.Stdout == "" && offset < len(text) {
		return fail(errors.New("token budget cannot admit the next character; increase --max-tokens"))
	}
	if _, err := io.WriteString(handler.Stdout, formatted.Stdout); err != nil {
		return err
	}
	next := offset + len(formatted.Stdout)
	if next < len(text) {
		_, _ = fmt.Fprintf(handler.Stderr, "hchanges: incomplete; repeat this read with --cursor %s:%d\n", digest, next)
		return interp.ExitStatus(1)
	}
	return nil
}
