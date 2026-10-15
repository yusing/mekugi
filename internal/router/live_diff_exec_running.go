package router

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/yusing/mekugi/internal/execscope"
	"github.com/yusing/mekugi/internal/execsegment"

	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/ui/diffview"
	"github.com/yusing/mekugi/internal/vcsguard"
)

// The window registry owns display cancellation, not process continuation.
// Source-card polling never sweeps or re-runs a provider query. Completed
// boundaries can compare private snapshots; neither path persists evidence.
var execRunningPreviewSlots = make(chan struct{}, 16)

const execRunningPreviewBytes = 1 << 20

func (r *execWindowRegistry) preview(ref string, observation execObservation, broker *liveDiffBroker, workspace, thread, caller string, snapshots *workspaceSnapshots) {
	// VCS effects use the same provenance admission as authored counts.
	// Only source-content projections admit paths, never generic watch effects.
	excludedVCS := func(origin string) bool {
		tool, _, _ := strings.Cut(origin, " ")
		return vcsguard.IsTool(tool) && !authoredReview(mekugi.ReviewFile{Origin: origin})
	}
	if r == nil || broker == nil {
		return
	}
	observed := observation
	projected := make(map[string]bool)
	commands := observation.Commands
	observation.Commands = nil
	for _, command := range commands {
		files, _ := execPreviewProjected(broker.ctx, execObservation{Files: observation.Files, Commands: []execCommandInput{command}})
		if len(files) == 0 {
			continue
		}
		observation.Commands = append(observation.Commands, command)
		for path := range files {
			projected[path] = true
		}
	}
	observation.Files = slices.DeleteFunc(slices.Clone(observation.Files), func(file execFileSnapshot) bool {
		streamable := projected[file.Path]
		return !streamable || file.watchMoveOnly || excludedVCS(file.Origin)
	})
	observation.Omitted = slices.DeleteFunc(slices.Clone(observation.Omitted), func(omission execOmission) bool { return excludedVCS(omission.Origin) })
	observation.Listings = slices.DeleteFunc(slices.Clone(observation.Listings), func(listing execListing) bool { return excludedVCS(listing.Origin) })
	if len(observation.Files) == 0 && r.tracker == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	window := r.find(ref)
	if window == nil || window.previewCancel != nil || !window.closed.IsZero() || window.background {
		return
	}
	select {
	case execRunningPreviewSlots <- struct{}{}:
	default:
		return
	}
	ctx, cancel := context.WithCancel(broker.ctx)
	window.previewCancel = cancel
	tracking := window.tracking
	if tracking == nil {
		tracking = newExecPreviewTrack(r.tracker, window.thread, window.turn, observed.Commands)
		window.tracking = tracking
	}
	if tracking != nil {
		tracking.streamable = make(map[string]bool)
		for _, command := range observation.Commands {
			tracking.streamable[tracking.authoredScript(command.Command, "")] = true
			segments, _ := execsegment.Split(command.Command)
			directory := command.Workdir
			for _, segment := range segments {
				statements, _, _, _ := liveDiffShellStatements(segment.Source, directory)
				if len(statements) == 1 {
					if next, ok := liveDiffShellCd(statements[0], directory); ok {
						directory = next
					}
				}
				input := command
				input.Command, input.Workdir = segment.Source, directory
				files, _ := execPreviewProjected(ctx, execObservation{Files: observation.Files, Commands: []execCommandInput{input}})
				if len(files) > 0 {
					tracking.streamable[tracking.authoredScript(segment.Source, "")] = true
				}
			}
		}
	}
	turn := window.turn
	go func() {
		defer func() { <-execRunningPreviewSlots }()
		capture := &execPreviewCapture{registry: r, ref: ref, observation: observed, snapshots: snapshots}
		runExecScopePreview(ctx, broker, observation, diffview.Preview{ID: "running:" + ref, Workspace: workspace, Thread: thread, Turn: turn, Caller: caller}, tracking, capture)
	}()
}

// Reuse captured program attribution, not the enclosing stock executor.
// A missing program label is unknown, not evidence that a shell edited it.
func execPreviewTool(observation execObservation) string {
	observation.Labels = slices.DeleteFunc(slices.Clone(observation.Labels), func(label string) bool { return label == "mv" || label == "git mv" })
	// The classifier's opaque-statement fallback is scope uncertainty,
	// not an evidenced executable. Keep it out of display attribution.
	observation.Programs = slices.DeleteFunc(slices.Clone(observation.Programs), func(program execscope.Program) bool {
		return program.Label == "shell"
	})
	tool := observation.sourceLabel()
	if tool == nativeExecCommandToolName {
		return ""
	}
	return tool
}

func execPreviewPaths(observation execObservation) []string {
	var paths []string
	for _, path := range observation.scopePaths() {
		if !slices.Contains(paths, path) {
			paths = append(paths, path)
		}
	}
	return paths
}

func execScopePreviewFooter(observation execObservation) string {
	paths := execPreviewPaths(observation)
	footer := fmt.Sprintf("may write · %d scoped paths", len(paths))
	if len(paths) > 0 {
		names := make([]string, 0, min(3, len(paths)))
		for _, path := range paths[:min(3, len(paths))] {
			names = append(names, filepath.Base(path))
		}
		footer += " · " + strings.Join(names, ", ")
	}
	if observation.Reason != "" || observation.Class == execscope.Opaque.String() {
		footer += " · other writes unknown"
	}
	if len(observation.Omitted) > 0 {
		footer += " · bounded scope"
	}
	return footer
}

// Reuse literal edit projection against the pre-call capture, never a fresh
// read of a file the host may already have edited. Unprojectable effects keep
// the normal host-result lifecycle; tests acquire no predicted write targets.
func execPreviewProjected(ctx context.Context, observation execObservation) (map[string]mekugi.ReviewFile, bool) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	sources := &liveDiffSources{capturedOnly: true, files: make(map[liveDiffSourceKey]liveDiffSource)}
	reader := reflect.ValueOf(liveDiffPreviewFile).Pointer()
	for _, file := range observation.Files {
		if file.Error == "" && (file.Kind == execFileText || file.Kind == execFileAbsent) && len(file.Content) <= liveDiffPreviewFileLimit {
			sources.files[liveDiffSourceKey{reader, file.Path}] = liveDiffSource{file.Content, file.Kind != execFileAbsent}
		}
	}
	worker := liveDiffPreviewWorker{ctx: context.WithValue(ctx, liveDiffSourcesContext{}, sources)}
	expected := make(map[string]mekugi.ReviewFile)
	complete := true
	for _, command := range observation.Commands {
		if ctx.Err() != nil {
			return nil, false
		}
		if shell := shellInterpreterName(command.Shell); shell != "" && shell != "bash" && shell != "sh" {
			complete = false
			continue
		}
		files, _, err := worker.projectShell(command.Command, command.Workdir, true)
		if err != nil {
			complete = false
			continue
		}
		for _, file := range files {
			path := file.AfterPath
			if path == "" {
				path = file.BeforePath
			}
			if _, duplicate := expected[path]; duplicate {
				complete = false
			}
			if file.Incomplete != "" || file.Binary || file.Link {
				complete = false
				continue
			}
			expected[path] = file
		}
	}
	return expected, complete
}

func execPreviewExpected(ctx context.Context, observation execObservation) map[string]mekugi.ReviewFile {
	expected, complete := execPreviewProjected(ctx, observation)
	// Only retire the watcher when its entire admitted edit scope is known.
	if !complete || len(expected) != len(observation.Files) {
		return nil
	}
	for _, file := range observation.Files {
		if _, exists := expected[file.Path]; !exists {
			return nil
		}
	}
	return expected
}

// The running watcher owns both source cards and completed Activity evidence.
// Only cards require prediction. Receipts compare the original capture scope.
type execPreviewCapture struct {
	registry    *execWindowRegistry
	ref         string
	observation execObservation
	snapshots   *workspaceSnapshots
	completed   int
	text        string
	calls       []string
	next        time.Time
}

func (c *execPreviewCapture) update(ctx context.Context, broker *liveDiffBroker, preview diffview.Preview, tracking *execPreviewTrack, state execPreviewState) {
	if state.completed <= c.completed || time.Now().Before(c.next) {
		return
	}
	c.next = time.Now().Add(500 * time.Millisecond)
	view := c.registry.view([]string{c.ref}, false)
	env := execReconcileEnv{excluded: view.excluded}
	// Only unnamed effects require an isolated workspace snapshot. A sibling
	// on another target must not suppress captured named baselines.
	if c.snapshots != nil && c.observation.Tree != "" && view.snapshot && len(view.overlaps) == 0 && len(view.background) == 0 {
		var err error
		env.snapshot, _, err = c.snapshots.since(ctx, preview.Workspace, c.observation.Tree, view.claims)
		if err != nil {
			return
		} // Keep the boundary pending for retry.
	}
	scope := c.observation
	scope.addCreatedPaths(view.created)
	// This receipt is an observed endpoint, not a claim of complete intermediate
	// write history. Returning to the baseline must withdraw the earlier endpoint.
	scope.RepeatedPaths = false
	files, complete, _, _ := reconcileExecObservation(scope, env)
	// Another writer may have started while the filesystem comparison ran.
	if current := tracking.state(); current.completed != state.completed || !slices.Equal(current.calls, state.calls) || ctx.Err() != nil {
		return
	}
	text := editReceiptText(preview.Workspace, mekugiHistory{Source: execPreviewTool(c.observation), ReviewFiles: files, ExecOutcome: &execOutcome{Labels: c.observation.Labels}})
	if (complete || text != "") && (text != c.text || text != "" && !slices.Equal(state.calls, c.calls)) {
		preview.ID, preview.Files = "observed:"+c.ref, files
		preview.EditReceipt, preview.HostItems, preview.Complete = text, slices.Clone(state.calls), true
		preview.Status, preview.Footer = "", "observed edit"
		broker.publishPreview(preview, false)
		c.text, c.calls = text, slices.Clone(state.calls)
	}
	if complete {
		c.completed = state.completed
	}
}

func runExecScopePreview(ctx context.Context, broker *liveDiffBroker, observation execObservation, preview diffview.Preview, tracking *execPreviewTrack, capture *execPreviewCapture) {
	preview.Tool = execPreviewTool(observation)
	defer broker.discardRunningPreview(preview)
	expected := execPreviewExpected(ctx, observation)
	matched := make(map[string]bool)
	footer := execScopePreviewFooter(observation)
	if len(expected) > 0 {
		// This card belongs to the projected edit, not arbitrary side effects
		// of a later test or other command sharing its host invocation.
		footer = execScopePreviewFooter(execObservation{Files: observation.Files})
	}
	preview.Footer = footer
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	stamps := make(map[string]string)
	changed := make(map[string]mekugi.ReviewFile)
	settledPaths := make(map[string]bool)
	cursor := 0
	for {
		state := tracking.state()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-state.changed:
		}
		state = tracking.state()
		if capture != nil && tracking != nil {
			capture.update(ctx, broker, preview, tracking, state)
		}
		if preview.Complete || len(observation.Files) == 0 {
			continue
		}
		tracked, settled := state.tracked, state.settled
		deadline := time.Now().Add(30 * time.Millisecond)
		budget := execRunningPreviewBytes
		updated := false
		for visited := 0; visited < len(observation.Files) && time.Now().Before(deadline) && budget > 0; visited++ {
			before := observation.Files[cursor]
			cursor = (cursor + 1) % len(observation.Files)
			if ctx.Err() != nil {
				return
			}
			info, err := os.Lstat(before.Path)
			stamp := "absent"
			if err == nil {
				stamp = execWatchFileStamp(before.Path, info)
			} else if !os.IsNotExist(err) && !errors.Is(err, syscall.ENOTDIR) {
				continue
			}
			// Copy scope includes both dst and dst/basename until the host
			// resolves file-vs-directory semantics. Once dst is a regular file,
			// its child candidate is absent, as in snapshotExecFile, not pending.
			if previous, ok := stamps[before.Path]; ok && previous == stamp {
				if settled {
					settledPaths[before.Path] = true
				}
				continue
			}
			after := snapshotExecFile(before.Path, &budget)
			if after.Error == "capture budget exhausted" {
				if after.Size <= execRunningPreviewBytes {
					continue
				}
				after.Error = "running preview exceeds the 1 MiB content bound; content not read"
			}
			stamps[before.Path] = stamp
			if settled {
				settledPaths[before.Path] = true
			}
			var file mekugi.ReviewFile
			if target, ok := expected[before.Path]; ok {
				beforePath, afterPath := before.Path, before.Path
				if !execFilePresent(before) {
					beforePath = ""
				}
				if !execFilePresent(after) {
					afterPath = ""
				}
				if before.Error == "" && after.Error == "" {
					file = renderExecReview(beforePath, afterPath, before, after)
				}
				comparison := file
				if file.Diff != "" && !file.Binary && !file.Link {
					// Predictions are compact; compare that projection without
					// shrinking the observed evidence shown below or retained later.
					comparison.Diff = mekugi.RenderReviewPreviewFile(beforePath, afterPath, execReviewText(before), execReviewText(after)).Diff
				}
				if comparison == target {
					matched[before.Path] = true
				} else {
					delete(matched, before.Path)
				}
			}
			if before.Kind == execFileOther || after.Kind == execFileOther ||
				after.Kind == execFileDir {
				_, existed := changed[before.Path]
				updated = updated || existed
				delete(changed, before.Path)
				continue
			}
			if before.Error != "" || after.Error != "" {
				if before.watchStamp != "" && before.watchStamp == stamp {
					_, existed := changed[before.Path]
					updated = updated || existed
					delete(changed, before.Path)
					continue
				}
				file = mekugi.RenderIncompleteReviewFile(before.Path, before.Path, strings.Trim(strings.Join([]string{before.Error, after.Error}, "; "), "; "))
			} else if sameExecContent(before, after) {
				_, existed := changed[before.Path]
				updated = updated || existed
				delete(changed, before.Path)
				continue
			} else {
				beforePath, afterPath := before.Path, before.Path
				if !execFilePresent(before) {
					beforePath = ""
				}
				if !execFilePresent(after) {
					afterPath = ""
				}
				if file.Diff == "" {
					file = renderExecReview(beforePath, afterPath, before, after)
				}
			}
			if previous, exists := changed[before.Path]; !exists || previous != file {
				changed[before.Path] = file
				updated = true
			}
		}
		preview.Files = nil
		for _, before := range observation.Files {
			if file, ok := changed[before.Path]; ok {
				preview.Files = append(preview.Files, file)
			}
		}
		// The literal edit has landed. A following test keeps the host command
		// alive, not this edit's live card. This does not close the observation
		// window or persist a command outcome or completed change receipt.
		if settled && len(settledPaths) == len(observation.Files) || !tracked && len(expected) > 0 && len(matched) == len(expected) {
			if len(preview.Files) > 0 && ctx.Err() == nil {
				preview.Status, preview.Complete = diffview.PreviewRunning, true
				preview.Footer = "observed edit"
				broker.publishPreview(preview, false)
			}
			if capture == nil || tracking == nil {
				return
			}
			preview.Complete = true
			continue
		}
		if len(preview.Files) == 0 {
			broker.discardRunningPreview(preview)
			preview.Status, preview.Input = "", ""
			continue
		}
		updated = updated || preview.Status != diffview.PreviewRunning
		preview.Status, preview.Input = diffview.PreviewRunning, ""
		preview.Footer = footer
		preview.Footer = strings.Replace(preview.Footer, "may write", "observed changes", 1)
		if !updated {
			// Admission can reject a new card while the broker is full. Keep
			// retrying until it is visible, even if its files remain unchanged.
			broker.mu.Lock()
			_, visible := broker.previews[preview.ID]
			broker.mu.Unlock()
			if visible {
				continue
			}
		}
		if ctx.Err() != nil {
			return
		}
		broker.publishPreview(preview, false)
	}
}

// A running card vanishes when it has no observed effect. Unlike completed
// input streams, it must not leave a completed placeholder behind.
func (b *liveDiffBroker) discardRunningPreview(preview diffview.Preview) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, active := b.previews[preview.ID]; active {
		delete(b.previews, preview.ID)
		b.emitPreviewLocked(diffview.Preview{ID: preview.ID, Workspace: preview.Workspace, Thread: preview.Thread})
	}
}
