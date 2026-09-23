package router

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi"
)

// The window registry owns display cancellation, not process continuation.
// Running previews never sweep, persist evidence, or re-run a provider query.
var execRunningPreviewSlots = make(chan struct{}, 16)

const execRunningPreviewBytes = 1 << 20

func (r *execWindowRegistry) preview(ref string, observation execObservation, broker *liveDiffBroker, workspace, thread, caller string) {
	if r == nil || broker == nil {
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
	go func() {
		defer func() { <-execRunningPreviewSlots }()
		runExecScopePreview(ctx, broker, observation, liveDiffPreview{ID: "running:" + ref, Workspace: workspace, Thread: thread, Caller: caller})
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
		footer += " · unresolved targets"
	}
	if len(observation.Omitted) > 0 {
		footer += " · bounded scope"
	}
	return footer
}

func execPendingScope(observation execObservation) string {
	verb := "may write"
	for _, command := range observation.Commands {
		words := strings.Fields(command.Command)
		if len(words) < 2 {
			continue
		}
		switch filepath.Base(words[0]) + " " + words[1] {
		case "git restore", "git reset", "svn revert", "hg revert":
			verb = "will restore (pending)"
		case "git clean":
			verb = "will delete (pending)"
		case "git switch", "git checkout":
			verb = "will switch (pending)"
		}
	}
	paths := execPreviewPaths(observation)
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

func runExecScopePreview(ctx context.Context, broker *liveDiffBroker, observation execObservation, preview liveDiffPreview) {
	defer func() {
		broker.mu.Lock()
		defer broker.mu.Unlock()
		delete(broker.previews, preview.ID)
		// A running observation disappears, rather than becoming the retained
		// STREAMING COMPLETE card used for finished argument streams.
		broker.emitPreviewLocked(liveDiffPreview{ID: preview.ID, Workspace: preview.Workspace, Thread: preview.Thread})
	}()
	preview.Status, preview.Input, preview.Footer = "PENDING · scoped effects", execPendingScope(observation), execScopePreviewFooter(observation)
	broker.publishPreview(preview, false)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	stamps := make(map[string]string)
	changed := make(map[string]mekugi.ReviewFile)
	cursor := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		deadline := time.Now().Add(30 * time.Millisecond)
		budget := execRunningPreviewBytes
		updated := preview.Status != "RUNNING · observed so far"
		for visited := 0; visited < len(observation.Files) && time.Now().Before(deadline) && budget > 0; visited++ {
			before := observation.Files[cursor]
			cursor = (cursor + 1) % len(observation.Files)
			if ctx.Err() != nil {
				return
			}
			info, err := os.Lstat(before.Path)
			stamp := "absent"
			if err == nil {
				stamp = execFileStamp(info)
				if change, _, _, ok := execFileTimes(before.Path); ok {
					stamp += fmt.Sprint(":", change.UnixNano())
				}
			} else if !os.IsNotExist(err) {
				continue
			}
			if previous, ok := stamps[before.Path]; ok && previous == stamp {
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
			var file mekugi.ReviewFile
			if before.Error != "" || after.Error != "" {
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
				file = renderExecReview(beforePath, afterPath, before, after)
			}
			if previous, exists := changed[before.Path]; !exists || previous != file {
				changed[before.Path] = file
				updated = true
			}
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
		preview.Files = nil
		for _, before := range observation.Files {
			if file, ok := changed[before.Path]; ok {
				preview.Files = append(preview.Files, file)
			}
		}
		preview.Status = "RUNNING · observed so far"
		preview.Input = "No scoped changes observed yet"
		if len(preview.Files) > 0 {
			preview.Input = ""
		}
		if ctx.Err() != nil {
			return
		}
		broker.publishPreview(preview, false)
	}
}
