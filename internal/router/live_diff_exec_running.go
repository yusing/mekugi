package router

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/ui/diffview"
	"mvdan.cc/sh/v3/syntax"
)

// The window registry owns display cancellation, not process continuation.
// Running previews never sweep, persist evidence, or re-run a provider query.
var execRunningPreviewSlots = make(chan struct{}, 16)

const execRunningPreviewBytes = 1 << 20

func (r *execWindowRegistry) preview(ref string, observation execObservation, broker *liveDiffBroker, workspace, thread, caller string) {
	if r == nil || broker == nil || len(observation.Files) == 0 && execPendingScope(observation) == "" {
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
	tracking := newExecPreviewTrack(r.tracker, window.thread, window.turn, observation.Commands)
	turn := window.turn
	go func() {
		defer func() { <-execRunningPreviewSlots }()
		runExecScopePreview(ctx, broker, observation, diffview.Preview{ID: "running:" + ref, Workspace: workspace, Thread: thread, Turn: turn, Caller: caller}, tracking)
	}()
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
	if observation.Reason != "" || observation.Class == execOpaque.String() {
		footer += " · other writes unknown"
	}
	if len(observation.Omitted) > 0 {
		footer += " · bounded scope"
	}
	return footer
}

func execPendingScope(observation execObservation) string {
	paths := execPreviewPaths(observation)
	if len(paths) == 0 {
		return ""
	}
	verb := ""
	for _, command := range observation.Commands {
		program, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command.Command), "")
		if err != nil {
			continue
		}
		syntax.Walk(program, func(node syntax.Node) bool {
			if _, declaration := node.(*syntax.FuncDecl); declaration {
				// A definition alone does not run its body. Actual writes still
				// appear through the running file watch.
				return false
			}
			call, ok := node.(*syntax.CallExpr)
			if !ok {
				return true
			}
			words, ok := literalArgs(call.Args)
			if !ok || len(words) < 2 {
				return true
			}
			name := filepath.Base(words[0])
			subcommand := words[1]
			if name == "git" {
				var parsed bool
				subcommand, _, _, parsed = gitSubcommand(words[1:])
				if !parsed {
					return true
				}
			}
			switch name + " " + subcommand {
			case "git restore", "git reset", "svn revert", "hg revert":
				verb = "will restore (pending)"
			case "git clean":
				verb = "will delete (pending)"
			case "git switch", "git checkout":
				verb = "will switch (pending)"
			}
			return true
		})
	}
	if verb == "" {
		return ""
	}
	var body strings.Builder
	body.WriteString(verb)
	for _, path := range paths[:min(32, len(paths))] {
		body.WriteString("\n" + path)
	}
	if len(paths) > 32 {
		fmt.Fprintf(&body, "\n+%d more", len(paths)-32)
	}
	return body.String()
}

// Reuse literal edit projection against the pre-call capture, never a fresh
// read of a file the host may already have edited. Unprojectable effects keep
// the normal host-result lifecycle; tests acquire no predicted write targets.
func execPreviewExpected(ctx context.Context, observation execObservation) map[string]mekugi.ReviewFile {
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
	for _, command := range observation.Commands {
		if ctx.Err() != nil {
			return nil
		}
		if shell := shellInterpreterName(command.Shell); shell != "" && shell != "bash" && shell != "sh" {
			return nil
		}
		files, _, err := worker.projectShell(command.Command, command.Workdir, true)
		if err != nil {
			return nil
		}
		for _, file := range files {
			path := file.AfterPath
			if path == "" {
				path = file.BeforePath
			}
			if _, duplicate := expected[path]; duplicate || file.Incomplete != "" || file.Binary || file.Link {
				return nil
			}
			expected[path] = file
		}
	}
	// Only retire the watcher when its entire captured edit scope is known.
	if len(expected) != len(observation.Files) {
		return nil
	}
	for _, file := range observation.Files {
		if _, exists := expected[file.Path]; !exists {
			return nil
		}
	}
	return expected
}

func runExecScopePreview(ctx context.Context, broker *liveDiffBroker, observation execObservation, preview diffview.Preview, tracking *execPreviewTrack) {
	defer tracking.close()
	defer broker.discardRunningPreview(preview)
	expected := execPreviewExpected(ctx, observation)
	matched := make(map[string]bool)
	pending := execPendingScope(observation)
	footer := execScopePreviewFooter(observation)
	if len(expected) > 0 {
		// This card belongs to the projected edit, not arbitrary side effects
		// of a later test or other command sharing its host invocation.
		footer = execScopePreviewFooter(execObservation{Files: observation.Files})
	}
	preview.Footer = footer
	if pending != "" {
		preview.Status, preview.Input = diffview.PreviewPending, pending
		broker.publishPreview(preview, false)
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	stamps := make(map[string]string)
	changed := make(map[string]mekugi.ReviewFile)
	settledPaths := make(map[string]bool)
	cursor := 0
	for {
		_, _, lifecycle := tracking.state()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-lifecycle:
		}
		tracked, settled, _ := tracking.state()
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
			} else if !os.IsNotExist(err) {
				continue
			}
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
				if file == target {
					matched[before.Path] = true
				} else {
					delete(matched, before.Path)
				}
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
				preview.Tool = nativeExecCommandToolName
				preview.Footer = "observed edit"
				broker.publishPreview(preview, false)
			}
			return
		}
		status, input := diffview.PreviewRunning, ""
		if len(preview.Files) == 0 {
			if pending == "" {
				broker.discardRunningPreview(preview)
				preview.Status, preview.Input = "", ""
				continue
			}
			status, input = diffview.PreviewPending, pending
		}
		updated = updated || preview.Status != status || preview.Input != input
		preview.Status, preview.Input = status, input
		preview.Footer = footer
		if status == diffview.PreviewRunning {
			preview.Footer = strings.Replace(preview.Footer, "may write", "observed changes", 1)
		}
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
