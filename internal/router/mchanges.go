package router

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/yusing/mekugi/libraries/legacycall"

	"github.com/yusing/mekugi/modules/outputrecovery/recovery"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/libraries/commandargs"

	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/pathdisplay"
)

const changesReadUsage = "mchanges --list [ID[..ID] ...] [--workspace DIR] [--max-tokens N] | mchanges [--mine | ID[..ID] ...] [--summary|--history|--net] [--workspace DIR] [--max-tokens N] [-- PATH ...] | mchanges revert|apply ID[..ID] ... [--workspace DIR] [--max-tokens N] [-- PATH ...]"

const maxChangeReadBytes = 64 << 20

type changeReadOptions struct {
	// Only initial reads collect unavailable targets; frozen recovery stays strict.
	unavailable   changeReadFailures
	overlapLabels map[string]string
	mine          bool
	view          string
	paths         []string
	workspace     string
	maxTokens     int
	ids           []string
}

// A nil collector makes dependency reads strict, as required by recovery and mutation.
type changeReadFailures map[string]error

func (failures changeReadFailures) skip(id string, err error) bool {
	if failures == nil {
		return false
	}
	failures[id] = err
	return true
}

func (s *mekugiReplayStore) readChangeRecord(workspace, id, correlation, call string) (replayRecord, error) {
	record, found, err := s.read(workspace, call, false)
	if err != nil {
		return replayRecord{}, fmt.Errorf("change %s: %w", id, err)
	}
	if !found || record.History.ChangeID != id || record.History.CorrelationID != correlation {
		return replayRecord{}, fmt.Errorf("change %s has a missing or inconsistent attempt", id)
	}
	return record, nil
}

func parseChangeRead(arguments []string, cwd string) (changeReadOptions, error) {
	options := changeReadOptions{workspace: cwd, maxTokens: 4000}
	seen := make(map[string]bool)
	var refs []string
	if len(arguments) > 0 && (arguments[0] == "revert" || arguments[0] == "apply") {
		options.view, arguments = arguments[0], arguments[1:]
	}
	for len(arguments) > 0 {
		arguments = commandargs.ExpandMaxTokensOption(arguments)
		flag := arguments[0]
		arguments = arguments[1:]
		if flag == "--" {
			if slices.Contains(arguments, "") {
				return options, errors.New("paths after -- must be nonempty")
			}
			options.paths = append(options.paths, arguments...)
			break
		}
		if !strings.HasPrefix(flag, "-") {
			start, _, _ := strings.Cut(flag, "..")
			if _, _, err := parseChangeID(start); err != nil {
				return options, fmt.Errorf("invalid change operand %q; paths belong after -- PATH", flag)
			}
			refs = append(refs, flag)
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
		case "--mine":
			if options.view == "revert" || options.view == "apply" {
				return options, errors.New("--mine is for reads; mutations require explicit change IDs")
			}
			options.mine = true
		case "--list", "--summary", "--history", "--net":
			if options.view == "revert" || options.view == "apply" {
				return options, fmt.Errorf("%s does not accept %s", options.view, flag)
			}
			if options.view != "" {
				return options, errors.New("choose one of --list, --summary, --history, or --net")
			}
			options.view = strings.TrimPrefix(flag, "--")
		case "--workspace", "--max-tokens":
			if len(arguments) == 0 {
				if flag == "--max-tokens" {
					return options, errors.New(commandargs.MaxTokensArgumentError)
				}
				return options, fmt.Errorf("%s requires a value", flag)
			}
			value := arguments[0]
			arguments = arguments[1:]
			switch flag {
			case "--workspace":
				options.workspace = value
			case "--max-tokens":
				number, err := commandargs.ParseMaxTokens(value)
				if err != nil {
					return options, err
				}
				options.maxTokens = number
			}
		default:
			return options, fmt.Errorf("unknown option %s; %s", flag, changesReadUsage)
		}
	}
	if options.view == "list" && len(options.paths) != 0 {
		return options, errors.New("--list does not accept paths")
	}
	if options.mine && len(refs) != 0 {
		return options, errors.New("--mine does not accept explicit change IDs")
	}
	if len(refs) == 0 {
		if options.view == "revert" || options.view == "apply" {
			return options, fmt.Errorf("mutations require explicit change IDs; %s", changesReadUsage)
		}
		options.mine = true
	}
	var err error
	if len(refs) != 0 {
		options.ids, err = expandChangeRefs(refs)
		if err != nil {
			return options, err
		}
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

func (s *mekugiReplayStore) readChanges(ctx context.Context, options changeReadOptions) (string, error) {
	text, _, err := s.readChangeView(ctx, options)
	return text, err
}

// partialChangeReadError accompanies usable output from independent change reads.
type partialChangeReadError struct{ error }

func (s *mekugiReplayStore) readChangeView(ctx context.Context, options changeReadOptions) (string, *changeReadSnapshot, error) {
	var text string
	var snapshot *changeReadSnapshot
	err := s.scoped(ctx).locked(ctx, func(store *mekugiReplayStore) error {
		var err error
		text, snapshot, err = store.readChangeViewLocked(ctx, options, store.retainFiles)
		return err
	})
	return text, snapshot, err
}

func (s *mekugiReplayStore) readChangeViewLocked(ctx context.Context, options changeReadOptions, retain func(...string) error) (string, *changeReadSnapshot, error) {
	if options.view == "list" && len(options.ids) == 0 {
		options.mine = true
	}
	var output string
	var snapshot *changeReadSnapshot
	var targetErrors []error
	err := func() error {
		index, err := s.readChangeIndex(options.workspace)
		if err != nil {
			return err
		}
		if options.mine {
			if s.session.Thread == "" {
				return errors.New("own-thread reads require a Codex thread identity")
			}
			options.ids, err = threadChangeIDs(index, s.session.Thread)
			if err != nil {
				return err
			}
		}
		snapshot = &changeReadSnapshot{Workspace: options.workspace, IDs: options.ids, Paths: options.paths, View: options.view, Frozen: true,
			Selected: make(map[string]trackedChange), Streams: index.Streams, Mine: options.mine, OverlapLabels: make(map[string]string)}
		options.overlapLabels = snapshot.OverlapLabels
		names := []string{changeIndexName(options.workspace, index.Namespace)}
		var readable []string
		missingStreams := make(map[string]bool)
		for position := 0; position < len(options.ids); position++ {
			id := options.ids[position]
			change, exists := index.Changes[id]
			if !exists {
				if !options.mine || options.view == "net" {
					stream, number, _ := parseChangeID(id)
					state, _ := missingChangeState(index, id)
					last := id
					for position+1 < len(options.ids) {
						next := options.ids[position+1]
						if next != changeHandle(stream, number+1) {
							break
						}
						nextState, _ := missingChangeState(index, next)
						if _, exists := index.Changes[next]; exists || nextState != state {
							break
						}
						last = next
						number++
						position++
					}
					targetErrors = append(targetErrors, missingChangeRangeError(index, id, last))
					missingStreams[stream] = true
				} else {
					readable = append(readable, id)
				}
				continue
			}
			var readErr error
			if options.view == "net" {
				if len(change.Calls) == 0 || change.RetiredCalls != 0 {
					readErr = fmt.Errorf("change %s has pending or retired history; cannot produce a complete --net view", id)
				}
			}
			if readErr != nil {
				targetErrors = append(targetErrors, readErr)
				continue
			}
			readable = append(readable, id)
			snapshot.Selected[id] = change
		}
		options.ids = readable
		options.unavailable = make(changeReadFailures)
		output, err = s.renderChanges(ctx, options, index)
		for _, id := range readable {
			if failure := options.unavailable[id]; failure != nil {
				targetErrors = append(targetErrors, failure)
				delete(snapshot.Selected, id)
			}
		}
		snapshot.IDs = slices.DeleteFunc(readable, func(id string) bool { return options.unavailable[id] != nil })
		for _, id := range snapshot.IDs {
			for _, call := range snapshot.Selected[id].Calls {
				names = append(names, legacycall.Name(options.workspace, call.ID, false))
			}
		}
		if retainErr := retain(names...); retainErr != nil {
			return retainErr
		}
		if len(snapshot.IDs) == 0 && len(targetErrors) > 0 && err == nil {
			output = availableChangeRanges(index, missingStreams)
		}
		if partial, ok := errors.AsType[*partialChangeReadError](err); ok {
			targetErrors = append(targetErrors, partial.error)
			return nil
		}
		return err
	}()
	if err == nil && len(targetErrors) > 0 {
		err = &partialChangeReadError{errors.Join(targetErrors...)}
	}
	return output, snapshot, err
}

func threadChangeIDs(index changeIndex, thread string) ([]string, error) {
	for position, stream := range index.Streams {
		if stream.Thread != thread {
			continue
		}
		if stream.Next > maxChangeReadBytes/32 {
			return nil, errors.New("change list exceeds 64 MiB; select explicit IDs")
		}
		ids := make([]string, stream.Next)
		for number := 1; number <= stream.Next; number++ {
			ids[number-1] = changeHandle(index.streamName(position), number)
		}
		return ids, nil
	}
	return nil, nil
}

func missingChangeState(index changeIndex, id string) (string, string) {
	name, number, _ := parseChangeID(id)
	for position, stream := range index.Streams {
		if index.streamName(position) != name {
			continue
		}
		if number <= stream.Next {
			return "retired", changeHandle(name, stream.Next)
		}
		return "unknown", changeHandle(name, stream.Next)
	}
	return "unknown", "none in this stream"
}

func missingChangeError(index changeIndex, id string) error {
	return missingChangeRangeError(index, id, id)
}

func missingChangeRangeError(index changeIndex, first, last string) error {
	id := first
	subject := "change " + id + " is"
	if first != last {
		subject = "changes " + first + ".." + last + " are"
	}
	state, latest := missingChangeState(index, id)
	reason := "retired by session retention"
	if state == "unknown" {
		reason = "never allocated (latest is " + latest + ")"
	}
	if latest == "none in this stream" {
		return fmt.Errorf("%s %s in workspace %q; check --workspace", subject, reason, index.Workspace)
	}
	return fmt.Errorf("%s %s", subject, reason)
}

// Recovery advertises retained IDs, not replacement evidence for a failed read.
// Restrict it to the requested streams and never bridge retired holes.
func availableChangeRanges(index changeIndex, streams map[string]bool) string {
	numbers := make(map[string][]int)
	for id := range index.Changes {
		stream, number, _ := parseChangeID(id)
		if streams[stream] {
			numbers[stream] = append(numbers[stream], number)
		}
	}
	var output strings.Builder
	for position := range index.Streams {
		stream := index.streamName(position)
		ids := numbers[stream]
		slices.Sort(ids)
		for start := 0; start < len(ids); {
			end := start
			for end+1 < len(ids) && ids[end+1] == ids[end]+1 {
				end++
			}
			fmt.Fprintf(&output, "available IDs: %s", changeHandle(stream, ids[start]))
			if end != start {
				fmt.Fprintf(&output, "..%s", changeHandle(stream, ids[end]))
			}
			output.WriteByte('\n')
			start = end + 1
		}
	}
	return output.String()
}

func (s *mekugiReplayStore) renderChanges(ctx context.Context, options changeReadOptions, index changeIndex) (string, error) {
	if options.view == "list" {
		return s.renderChangeList(ctx, options, index)
	}
	if options.view == "net" {
		return s.renderNetChanges(ctx, options, index)
	}
	var output strings.Builder
	type counts struct {
		added, removed int
		incomplete     bool
		reasons        []string
		status         diffmodel.Status
		composition    diffmodel.ReviewComposition
		uncomposable   bool
		directory      bool
	}
	displayPath := func(path string) string {
		if strings.IndexFunc(path, unicode.IsControl) >= 0 || strings.ContainsAny(path, "\"\\") || strings.Contains(path, " => ") {
			return strconv.Quote(path)
		}
		return path
	}
	stats := make(map[string]*counts)
	var entries []*counts
	matched := false
	var summary []changeCapture
	summaryBytes := 0
	for _, id := range options.ids {
		if output.Len() > maxChangeReadBytes {
			return "", errors.New("change read exceeds 64 MiB; narrow the range, view, or paths after --")
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		change, exists := index.Changes[id]
		if !exists {
			if options.view != "summary" && !options.mine {
				return "", missingChangeError(index, id)
			}
			state, _ := missingChangeState(index, id)
			fmt.Fprintf(&output, "%s %s\n", id, state)
			continue
		}
		records := make([]replayRecord, len(change.Calls))
		excludedOnly := len(change.Calls) != 0
		var readErr error
		for i, call := range change.Calls {
			record, err := s.readChangeRecord(options.workspace, id, change.Correlation, call.ID)
			if err != nil {
				readErr = err
				break
			}
			records[i] = record
			excludedOnly = excludedOnly && len(record.History.ReviewFiles) != 0 && !slices.ContainsFunc(record.History.ReviewFiles, authoredReview)
		}
		if readErr != nil {
			if options.unavailable.skip(id, readErr) {
				continue
			}
			return "", readErr
		}
		if options.view != "history" && change.RetiredCalls == 0 && excludedOnly {
			continue
		}
		if options.view == "summary" && change.RetiredCalls != 0 {
			fmt.Fprintf(&output, "%s retired (partial history)\n", id)
		}
		if change.RetiredCalls != 0 && options.view != "summary" {
			fmt.Fprintf(&output, "%s history incomplete: %d older attempts were removed by session cleanup\n", id, change.RetiredCalls)
		}
		if options.view == "history" && len(change.Calls) > 1 {
			fmt.Fprintf(&output, "%s attempts=%d\n", id, len(change.Calls))
		}
		if options.view != "summary" && options.view != "history" && len(change.Calls) != 0 {
			fmt.Fprintln(&output, id)
		}
		if len(change.Calls) == 0 {
			fmt.Fprintf(&output, "%s pending (no completed result)\n", id)
		}
		for position, record := range records {
			history := record.History
			if options.view != "history" {
				history = authoredChangeHistory(history)
			}
			if options.view == "history" && history.ExecOutcome != nil && len(history.ExecOutcome.Overlaps) != 0 {
				var alongside []string
				for _, ref := range history.ExecOutcome.Overlaps {
					label, frozen := options.overlapLabels[ref]
					if !frozen {
						label = ref
						for otherID, other := range index.Changes {
							if slices.ContainsFunc(other.Calls, func(call trackedCall) bool { return strings.HasPrefix(call.ID, ref+":") }) {
								label = otherID
								break
							}
						}
						if options.overlapLabels != nil {
							options.overlapLabels[ref] = label
						}
					}
					alongside = append(alongside, label)
				}
				fmt.Fprintf(&output, "observed alongside %s\n", strings.Join(alongside, ", "))
			}
			status := "no changes"
			if history.ExecOutcome != nil && history.ExecOutcome.Coverage != "" && history.ExecOutcome.Coverage != execCoverageExact {
				status = "incomplete captured scope"
				if options.view == "summary" || options.view == "" {
					fmt.Fprintf(&output, "%s incomplete captured scope\n", id)
				}
			}
			if slices.ContainsFunc(history.ReviewFiles, authoredReview) {
				status = "applied"
			}
			if options.view == "history" && len(change.Calls) == 1 {
				fmt.Fprintf(&output, "%s %s\n", id, status)
			} else if options.view == "history" {
				fmt.Fprintf(&output, "attempt %d %s\n", position+1, status)
			}
			if options.view == "history" {
				for _, result := range history.HostResults {
					fmt.Fprintln(&output, result.text())
				}
				if history.ExecOutcome != nil {
					fmt.Fprintln(&output, history.ExecOutcome.text())
				}
				fmt.Fprintf(&output, "%s input:\n%s\n", history.ToolName, history.Script)
				if history.ExecOutcome != nil && history.ExecOutcome.ScopeReason != "" {
					fmt.Fprintf(&output, "scope: %s\n", history.ExecOutcome.ScopeReason)
				}
				if history.ExecOutcome != nil && len(history.ExecOutcome.Scope) != 0 {
					scope := make([]string, 0, len(history.ExecOutcome.Scope))
					for _, path := range history.ExecOutcome.Scope {
						scope = append(scope, displayPath(pathdisplay.ForWorkspace(options.workspace, path)))
					}
					fmt.Fprintf(&output, "observed scope: %s\n", strings.Join(scope, ", "))
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
			for _, file := range history.ReviewFiles {
				if options.view != "history" && !authoredReview(file) {
					continue
				}
				if len(options.paths) > 0 && !changePathMatches(options, file.BeforePath) && !changePathMatches(options, file.AfterPath) {
					continue
				}
				matched = true
				if options.view == "summary" {
					summaryBytes += len(file.Diff)
					if summaryBytes > maxChangeReadBytes {
						return "", errors.New("change summary exceeds 64 MiB; narrow the range or paths after --")
					}
					summary = append(summary, changeCapture{order: record.CaptureOrder, files: []diffmodel.ReviewFile{file}})
				} else if file.Directory {
					path := cmp.Or(file.AfterPath, file.BeforePath)
					fmt.Fprintf(&output, "%s %s/\n", diffmodel.StatusOf(file).ShortCode(), displayPath(pathdisplay.ForWorkspace(options.workspace, path)))
				} else {
					if file.OriginNote != "" {
						fmt.Fprintln(&output, file.OriginNote)
					}
					output.WriteString(file.UnifiedDiffWithPaths(pathdisplay.ForWorkspace(options.workspace, file.BeforePath), pathdisplay.ForWorkspace(options.workspace, file.AfterPath)))
				}
			}
			if output.Len() > maxChangeReadBytes {
				return "", errors.New("change read exceeds 64 MiB; narrow the range, view, or paths after --")
			}
		}
	}
	slices.SortStableFunc(summary, func(a, b changeCapture) int { return cmp.Compare(a.order, b.order) })
	for _, capture := range summary {
		file := capture.files[0]
		key := func(path string) string {
			path = pathdisplay.ForWorkspace(options.workspace, path)
			return path
		}
		before, after := file.BeforePath, file.AfterPath
		if before == "" {
			before = after
		}
		entry := stats[key(before)]
		if entry == nil {
			entry = &counts{status: diffmodel.Status{Before: file.BeforePath}}
			entries = append(entries, entry)
		}
		delete(stats, key(before))
		if after == "" {
			after = before
		}
		stats[key(after)] = entry
		entry.directory = entry.directory || file.Directory
		entry.status.Add(file)
		if file.Incomplete != "" && !slices.Contains(entry.reasons, file.Incomplete) {
			entry.reasons = append(entry.reasons, file.Incomplete)
		}
		if !entry.uncomposable {
			entry.uncomposable = entry.composition.ApplyWithHighlight(file, false, false) != nil
		}
		added, removed := file.LineCounts()
		if added < 0 || file.Binary {
			// Binary content has no row counts, as in git --numstat.
			entry.incomplete = true
		} else {
			entry.added += added
			entry.removed += removed
		}
	}
	for _, entry := range entries {
		if entry.directory {
			if entry.status.Before == "" && entry.status.After == "" {
				continue
			}
			path := cmp.Or(entry.status.After, entry.status.Before)
			fmt.Fprintf(&output, "%s %s/\n", entry.status.ShortCode(), displayPath(pathdisplay.ForWorkspace(options.workspace, path)))
			continue
		}
		if !entry.uncomposable && entry.status.Before == "" && entry.status.After == "" {
			continue // A creation followed by deletion leaves no file in the diff pane.
		}
		if !entry.uncomposable {
			var regions []diffmodel.ReviewFile
			entry.added, entry.removed = 0, 0
			for _, region := range entry.composition.FilesWithHighlights() {
				regions = append(regions, region.ReviewFile)
				added, removed := region.ReviewFile.LineCounts()
				entry.added += added
				entry.removed += removed
			}
			composed := diffmodel.StatusOf(regions...)
			entry.status.Edited, entry.status.Conflict = composed.Edited, composed.Conflict
		} else {
			if !entry.incomplete && len(entry.reasons) == 0 {
				entry.reasons = append(entry.reasons, "captured changes cannot be composed")
				entry.status.Incomplete = true
			}
			entry.incomplete = true
		}
		if !entry.incomplete && entry.added == 0 && entry.removed == 0 && entry.status.Before == entry.status.After {
			continue
		}
		path := entry.status.After
		if path == "" {
			path = entry.status.Before
		}
		path = displayPath(pathdisplay.ForWorkspace(options.workspace, path))
		if entry.status.Before != "" && entry.status.After != "" && entry.status.Before != entry.status.After {
			path = displayPath(pathdisplay.ForWorkspace(options.workspace, entry.status.Before)) + " => " + path
		}
		if entry.incomplete {
			fmt.Fprintf(&output, "%s\t-\t-\t%s", entry.status.ShortCode(), path)
			if len(entry.reasons) != 0 {
				fmt.Fprintf(&output, "\t%q", strings.Join(entry.reasons, "; "))
			}
			output.WriteByte('\n')
		} else {
			fmt.Fprintf(&output, "%s\t%d\t%d\t%s\n", entry.status.ShortCode(), entry.added, entry.removed, path)
		}
		if output.Len() > maxChangeReadBytes {
			return "", errors.New("change read exceeds 64 MiB; narrow the range, view, or paths after --")
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
func changePathMatches(options changeReadOptions, recorded string) bool {
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
		if path == recorded || (options.workspace != "" && resolve(path) == resolve(recorded)) {
			return true
		}
	}
	return false
}

func executeMChanges(ctx context.Context, manifest toolWorkerManifest, arguments []string) command.Output {
	fail := func(err error) command.Output {
		return command.Output{Stderr: fmt.Sprintf("mchanges: %v\n", err), ExitCode: 1}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fail(fmt.Errorf("resolve working directory: %w", err))
	}
	options, err := parseChangeRead(arguments, cwd)
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
	store := &mekugiReplayStore{publication: manifest.history.Attach(manifest.ReplayDirectory), recovery: manifest.recovery, directory: manifest.ReplayDirectory}
	if options.view == "revert" || options.view == "apply" {
		text, status, err := store.mutateChanges(ctx, options)
		if err != nil {
			return fail(err)
		}
		// A continuation must not repeat the mutation, so the remainder is
		// retained as plain output instead of a re-rendered change read. The
		// workspace has already changed; any paging failure keeps the full report.
		whole := func(err error) command.Output {
			return command.Output{Stdout: text, Stderr: "mchanges: report not paged: " + err.Error() + "\n", ExitCode: status}
		}
		selected, err := selectReadPage(ctx, text, options.maxTokens)
		if err != nil {
			return whole(err)
		}
		execution := command.Output{Stdout: selected, ExitCode: status}
		if len(selected) < len(text) {
			execution.OmittedOutput = &command.OmittedOutput{Stdout: text[len(selected):], StdoutKind: "rows"}
			execution.ExitCode = 1
			if execution, err = retainExecutionOutput(ctx, manifest, execution); err != nil {
				return whole(err)
			}
		}
		return execution
	}
	text, snapshot, err := store.readChangeView(ctx, options)
	diagnostics := ""
	if err != nil {
		if _, partial := errors.AsType[*partialChangeReadError](err); !partial {
			return fail(err)
		}
		diagnostics = fmt.Sprintf("mchanges: %v\n", err)
	}
	selected, err := selectReadPage(ctx, text, options.maxTokens)
	if err != nil {
		return fail(err)
	}
	if diagnostics != "" {
		execution := command.Output{Stdout: selected, Stderr: diagnostics, ExitCode: 1}
		if len(selected) < len(text) {
			execution.OmittedOutput = &command.OmittedOutput{Stdout: text[len(selected):], StdoutKind: "rows"}
			retained, err := retainExecutionOutput(ctx, manifest, execution)
			if err != nil {
				return command.Output{Stdout: selected, Stderr: diagnostics + fmt.Sprintf("mchanges: recovery unavailable: %v\n", err), ExitCode: 1}
			}
			return retained
		}
		return execution
	}
	next := ""
	if len(selected) < len(text) {
		next, err = store.putChangeRead(ctx, snapshot, text, len(selected))
		if err != nil {
			return command.Output{Stdout: selected, Stderr: fmt.Sprintf("mchanges: recovery unavailable: %v\n", err), ExitCode: 1}
		}
	}
	if next != "" {
		return command.Output{Stdout: selected, Stderr: recovery.NextCall(next), ExitCode: 1}
	}
	return command.Output{Stdout: selected}
}
