package router

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/hpatchsyntax"
	"mvdan.cc/sh/v3/interp"
)

const changesReadUsage = "hchanges ID[..ID] ... [--summary|--history] [--workspace DIR] [--max-tokens N] [-- PATH ...]"
const maxChangeReadBytes = 64 << 20

type changeReadOptions struct {
	view      string
	paths     []string
	workspace string
	maxTokens int
	ids       []string
}

func parseChangeRead(arguments []string, cwd string) (changeReadOptions, error) {
	options := changeReadOptions{workspace: cwd, maxTokens: 4000}
	seen := make(map[string]bool)
	var refs []string
	for len(arguments) > 0 {
		flag := arguments[0]
		arguments = arguments[1:]
		if flag == "--" {
			if slices.Contains(arguments, "") {
				return options, errors.New("paths after -- must be nonempty")
			}
			options.paths = append(options.paths, arguments...)
			break
		}
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
		case "--workspace", "--max-tokens":
			if len(arguments) == 0 {
				return options, fmt.Errorf("%s requires a value", flag)
			}
			value := arguments[0]
			arguments = arguments[1:]
			switch flag {
			case "--workspace":
				options.workspace = value
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
		return options, fmt.Errorf("explicit change IDs or ranges are required; %s", changesReadUsage)
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
	if history.TranslationError == "" && history.ToolName == mekugiToolName {
		// Carrier kind identifies transport, not whether this retained script
		// hands off a mixed execution plan. Use the same framing owner as translation.
		_, mixed, _ := hpatchsyntax.SplitShell(history.Script)
		if mixed || strings.HasPrefix(strings.TrimSpace(history.Script), "resume ") {
			return "execution plan (see segment attempts)"
		}
	}
	switch {
	case history.TranslationError != "":
		return "rejected"
	case history.AlreadySatisfied:
		return "no-op"
	case history.Applied || confirmed:
		return "applied"
	default:
		return "prepared (application unconfirmed)"
	}
}

func (s *mekugiReplayStore) readChanges(ctx context.Context, options changeReadOptions) (string, error) {
	s = s.scoped(ctx)
	var output string
	err := s.locked(ctx, func() error {
		index, err := s.readChangeIndex(options.workspace)
		if err != nil {
			return err
		}
		names, err := s.changeDependencyNames(options.workspace, options.ids)
		if err != nil {
			return err
		}
		if err := s.retainFiles(names...); err != nil {
			return err
		}
		output, err = s.renderChanges(ctx, options, index)
		return err
	})
	return output, err
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
			return "", fmt.Errorf("change %s is missing in workspace %q; check --workspace; session data may have expired after 14 days of inactivity or been removed under storage pressure", id, options.workspace)
		}
		if change.RetiredCalls != 0 {
			fmt.Fprintf(&output, "%s history incomplete: %d older attempts were removed by session cleanup\n", id, change.RetiredCalls)
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
			history := record.History
			if len(change.Calls) == 1 {
				fmt.Fprintf(&output, "%s %s\n", id, trackedStatus(history, call.Confirmed))
			} else {
				fmt.Fprintf(&output, "attempt %d %s\n", position+1, trackedStatus(history, call.Confirmed))
			}
			retained := history.Applied && strings.HasPrefix(strings.TrimLeft(history.recoveryBaseline(), "\r\n"), "in @shell/")
			if retained {
				output.WriteString("scope: retained shell script, not workspace files\n")
			}
			if options.view == "history" {
				fmt.Fprintf(&output, "%s input:\n%s\n", history.ToolName, history.Script)
				if history.Evaluated != "" {
					fmt.Fprintf(&output, "evaluated script:\n%s\n", history.Evaluated)
				}
				if history.Report != "" {
					output.WriteString(strings.TrimPrefix(history.Report, changeNotice(id)))
					output.WriteByte('\n')
				}
				if history.TranslationError != "" {
					output.WriteString(strings.TrimPrefix(history.TranslationError, changeNotice(id)))
					output.WriteByte('\n')
				}
			}
			var summaryFiles []mekugi.ReviewFile
			for _, file := range history.ReviewFiles {
				if len(options.paths) > 0 && !changePathMatches(options, file.BeforePath, retained) && !changePathMatches(options, file.AfterPath, retained) {
					continue
				}
				matched = true
				if options.view == "summary" {
					summaryFiles = append(summaryFiles, file)
				} else {
					output.WriteString(file.UnifiedDiff())
				}
			}
			if options.view == "summary" {
				output.WriteString(mekugi.ReviewStat(summaryFiles))
			}
			if output.Len() > maxChangeReadBytes {
				return "", errors.New("change read exceeds 64 MiB; narrow the range, view, or paths after --")
			}
		}
	}
	if len(options.paths) > 0 && !matched {
		output.WriteString("no files match paths after --:")
		for _, path := range options.paths {
			fmt.Fprintf(&output, " %q", path)
		}
		output.WriteByte('\n')
	}
	return output.String(), nil
}

// Match lexical workspace-relative and absolute spellings without consulting
// current files: historical paths may have been moved or deleted since capture.
func changePathMatches(options changeReadOptions, recorded string, retained bool) bool {
	if recorded == "" {
		return false
	}
	resolve := func(path string) string {
		if !filepath.IsAbs(path) {
			path = filepath.Join(options.workspace, path)
		}
		return filepath.Clean(path)
	}
	for _, path := range options.paths {
		if path == recorded || (!retained && options.workspace != "" && resolve(path) == resolve(recorded)) {
			return true
		}
	}
	return false
}

func executeHChanges(ctx context.Context, manifest toolWorkerManifest, runtimeRoot string, arguments []string) error {
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
	selected, err := selectReadPage(ctx, manifest, runtimeRoot, text, options.maxTokens)
	if err != nil {
		return fail(err)
	}
	next := ""
	if len(selected) < len(text) {
		next, err = store.putChangeRead(ctx, options, text, len(selected))
		if err != nil {
			return fail(err)
		}
	}
	if _, err := io.WriteString(handler.Stdout, selected); err != nil {
		return err
	}
	if next != "" {
		_, _ = io.WriteString(handler.Stderr, readNextCall(next))
		return interp.ExitStatus(1)
	}
	return nil
}
