package preview

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/yusing/mekugi/libraries/commandanalysis"
	"github.com/yusing/mekugi/libraries/commandanalysis/execscope"
	"github.com/yusing/mekugi/libraries/commandanalysis/shellsyntax"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/shellinstrumentation"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/livepreview/projection"
)

const endpointReadBytes = 1 << 20

// SelectEndpoints admits predicted source-content paths without changing the capture.
// Source: apps/mekugi/internal/router/live_diff_exec_running.go:34:66@80a1714b.
func SelectEndpoints(ctx context.Context, observation changecapture.Observation) changecapture.Observation {
	// VCS effects use the same provenance admission as authored counts.
	// Only source-content projections admit paths, never generic watch effects.
	excludedVCS := func(origin string) bool {
		tool, _, _ := strings.Cut(origin, " ")
		return commandanalysis.IsVCSTool(tool) && !changes.AuthoredReview(diffmodel.ReviewFile{Origin: origin})
	}
	projected := make(map[string]bool)
	commands := observation.Commands
	observation.Commands = nil
	for _, command := range commands {
		files, _ := ProjectCaptured(ctx, changecapture.Observation{Files: observation.Files, Commands: []commandanalysis.Command{command}})
		if len(files) == 0 {
			continue
		}
		observation.Commands = append(observation.Commands, command)
		for path := range files {
			projected[path] = true
		}
	}
	observation.Files = slices.DeleteFunc(slices.Clone(observation.Files), func(file changecapture.File) bool {
		streamable := projected[file.Path]
		return !streamable || file.WatchMoveOnly || excludedVCS(file.Origin)
	})
	observation.Omitted = slices.DeleteFunc(slices.Clone(observation.Omitted), func(omission changecapture.Omission) bool { return excludedVCS(omission.Origin) })
	observation.Listings = slices.DeleteFunc(slices.Clone(observation.Listings), func(listing changecapture.Listing) bool { return excludedVCS(listing.Origin) })
	return observation
}

// Reuse captured program attribution, not the enclosing stock executor.
// A missing program label is unknown, not evidence that a shell edited it.
// Source: apps/mekugi/internal/router/live_diff_exec_running.go:114:216@80a1714b.
func CaptureTool(observation changecapture.Observation) string {
	observation.Labels = slices.DeleteFunc(slices.Clone(observation.Labels), func(label string) bool { return label == "mv" || label == "git mv" })
	// The classifier's opaque-statement fallback is scope uncertainty,
	// not an evidenced executable. Keep it out of display attribution.
	observation.Programs = slices.DeleteFunc(slices.Clone(observation.Programs), func(program execscope.Program) bool {
		return program.Label == "shell"
	})
	tool := observation.SourceLabel()
	if tool == "exec_command" {
		return ""
	}
	return tool
}

func capturePaths(observation changecapture.Observation) []string {
	var paths []string
	for _, path := range observation.ScopePaths() {
		if !slices.Contains(paths, path) {
			paths = append(paths, path)
		}
	}
	return paths
}

func CaptureFooter(observation changecapture.Observation) string {
	paths := capturePaths(observation)
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
func ProjectCaptured(ctx context.Context, observation changecapture.Observation) (map[string]diffmodel.ReviewFile, bool) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	ctx = projection.WithSources(ctx, true)
	for _, file := range observation.Files {
		if file.Error == "" && (file.Kind == changecapture.Text || file.Kind == changecapture.Absent) && len(file.Content) <= projection.FileLimit {
			projection.Prime(ctx, projection.ReadFile, file.Path, file.Content, file.Kind != changecapture.Absent)
		}
	}
	expected := make(map[string]diffmodel.ReviewFile)
	complete := true
	for _, command := range observation.Commands {
		if ctx.Err() != nil {
			return nil, false
		}
		if shell := shellsyntax.InterpreterIdentity(command.Shell); shell != "" && shell != "bash" && shell != "sh" {
			complete = false
			continue
		}
		files, _, _, err := projection.Shell(ctx, command.Command, command.Workdir, true)
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

func Expected(ctx context.Context, observation changecapture.Observation) map[string]diffmodel.ReviewFile {
	expected, complete := ProjectCaptured(ctx, observation)
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

// Endpoint owns one admitted source card, independently of host completion.
// Source: apps/mekugi/internal/router/live_diff_exec_running.go:272:446@80a1714b.
type Endpoint struct {
	broker       *Session
	observation  changecapture.Observation
	preview      Preview
	expected     map[string]diffmodel.ReviewFile
	matched      map[string]bool
	stamps       map[string]string
	changed      map[string]diffmodel.ReviewFile
	settledPaths map[string]bool
	footer       string
	cursor       int
}

func (b *Session) ObserveEndpoints(ctx context.Context, observation changecapture.Observation, frame Preview) *Endpoint {
	frame.Tool = CaptureTool(observation)
	expected := Expected(ctx, observation)
	footer := CaptureFooter(observation)
	if len(expected) > 0 {
		footer = CaptureFooter(changecapture.Observation{Files: observation.Files})
	}
	frame.Footer = footer
	return &Endpoint{broker: b, observation: observation, preview: frame, expected: expected, footer: footer,
		matched: make(map[string]bool), stamps: make(map[string]string), changed: make(map[string]diffmodel.ReviewFile), settledPaths: make(map[string]bool)}
}

func (w *Endpoint) View() Preview {
	frame := w.preview
	frame.Files = slices.Clone(frame.Files)
	frame.HostItems = slices.Clone(frame.HostItems)
	return frame
}

func (w *Endpoint) Close() { w.broker.DiscardRunning(w.preview) }

// Advance samples only admitted endpoints and reports source-card settlement.
// It does not close host windows or decide retained-evidence completion.
func (w *Endpoint) Advance(ctx context.Context, tracked, settled bool) bool {
	if w.preview.Complete || len(w.observation.Files) == 0 {
		return w.preview.Complete
	}
	deadline := time.Now().Add(30 * time.Millisecond)
	budget := endpointReadBytes
	updated := false
	for visited := 0; visited < len(w.observation.Files) && time.Now().Before(deadline) && budget > 0; visited++ {
		before := w.observation.Files[w.cursor]
		w.cursor = (w.cursor + 1) % len(w.observation.Files)
		if ctx.Err() != nil {
			return false
		}
		info, err := os.Lstat(before.Path)
		stamp := "absent"
		if err == nil {
			stamp = changecapture.WatchFileStamp(before.Path, info)
		} else if !os.IsNotExist(err) && !errors.Is(err, syscall.ENOTDIR) {
			continue
		}
		// Copy scope includes both dst and dst/basename until the host
		// resolves file-vs-directory semantics. Once dst is a regular file,
		// its child candidate is absent, as in changecapture.ReadFile, not pending.
		if previous, ok := w.stamps[before.Path]; ok && previous == stamp {
			if settled {
				w.settledPaths[before.Path] = true
			}
			continue
		}
		after := w.broker.readEndpoint(before.Path, &budget)
		if after.Error == "capture budget exhausted" {
			if after.Size <= endpointReadBytes {
				continue
			}
			after.Error = "running preview exceeds the 1 MiB content bound; content not read"
		}
		w.stamps[before.Path] = stamp
		if settled {
			w.settledPaths[before.Path] = true
		}
		var file diffmodel.ReviewFile
		if target, ok := w.expected[before.Path]; ok {
			beforePath, afterPath := before.Path, before.Path
			if !changecapture.FilePresent(before) {
				beforePath = ""
			}
			if !changecapture.FilePresent(after) {
				afterPath = ""
			}
			if before.Error == "" && after.Error == "" {
				file = changecapture.RenderReview(beforePath, afterPath, before, after)
			}
			comparison := file
			if file.Diff != "" && !file.Binary && !file.Link {
				// Predictions are compact; compare that projection without
				// shrinking the observed evidence shown below or retained later.
				comparison.Diff = diffmodel.RenderReviewPreviewFile(beforePath, afterPath, changecapture.ReviewText(before), changecapture.ReviewText(after)).Diff
			}
			if comparison == target {
				w.matched[before.Path] = true
			} else {
				delete(w.matched, before.Path)
			}
		}
		if before.Kind == changecapture.Other || after.Kind == changecapture.Other ||
			after.Kind == changecapture.Directory {
			_, existed := w.changed[before.Path]
			updated = updated || existed
			delete(w.changed, before.Path)
			continue
		}
		if before.Error != "" || after.Error != "" {
			if before.WatchStamp != "" && before.WatchStamp == stamp {
				_, existed := w.changed[before.Path]
				updated = updated || existed
				delete(w.changed, before.Path)
				continue
			}
			file = diffmodel.RenderIncompleteReviewFile(before.Path, before.Path, strings.Trim(strings.Join([]string{before.Error, after.Error}, "; "), "; "))
		} else if changecapture.SameContent(before, after) {
			_, existed := w.changed[before.Path]
			updated = updated || existed
			delete(w.changed, before.Path)
			continue
		} else {
			beforePath, afterPath := before.Path, before.Path
			if !changecapture.FilePresent(before) {
				beforePath = ""
			}
			if !changecapture.FilePresent(after) {
				afterPath = ""
			}
			if file.Diff == "" {
				file = changecapture.RenderReview(beforePath, afterPath, before, after)
			}
		}
		if previous, exists := w.changed[before.Path]; !exists || previous != file {
			w.changed[before.Path] = file
			updated = true
		}
	}
	w.preview.Files = nil
	for _, before := range w.observation.Files {
		if file, ok := w.changed[before.Path]; ok {
			w.preview.Files = append(w.preview.Files, file)
		}
	}
	// The literal edit has landed. A following test keeps the host command
	// alive, not this edit's live card. This does not close the observation
	// window or persist a command outcome or completed change receipt.
	if settled && len(w.settledPaths) == len(w.observation.Files) || !tracked && len(w.expected) > 0 && len(w.matched) == len(w.expected) {
		if len(w.preview.Files) > 0 && ctx.Err() == nil {
			w.preview.Status, w.preview.Complete = PreviewRunning, true
			w.preview.Footer = "observed edit"
			w.broker.Update(w.preview, false)
		}
		w.preview.Complete = true
		return true
	}
	if len(w.preview.Files) == 0 {
		w.broker.DiscardRunning(w.preview)
		w.preview.Status, w.preview.Input = "", ""
		return false
	}
	updated = updated || w.preview.Status != PreviewRunning
	w.preview.Status, w.preview.Input = PreviewRunning, ""
	w.preview.Footer = w.footer
	w.preview.Footer = strings.Replace(w.preview.Footer, "may write", "observed changes", 1)
	if !updated {
		// Admission can reject a new card while the broker is full. Keep
		// retrying until it is visible, even if its files remain unchanged.
		visible := slices.ContainsFunc(w.broker.Current(), func(frame Preview) bool { return frame.ID == w.preview.ID })
		if visible {
			return false
		}
	}
	if ctx.Err() != nil {
		return false
	}
	w.broker.Update(w.preview, false)
	return false
}

// StreamableSources selects writer source for the application's existing report
// correlation. It creates no subscription or execution identity.
// Source: apps/mekugi/internal/router/live_diff_exec_running.go:80:105@80a1714b.
func StreamableSources(ctx context.Context, observation changecapture.Observation) []string {
	var sources []string
	for _, command := range observation.Commands {
		sources = append(sources, command.Command)
		segments, _ := shellinstrumentation.Split(command.Command)
		directory := command.Workdir
		for _, segment := range segments {
			statements, _, _, _ := shellsyntax.StreamingStatements(segment.Source, directory)
			if len(statements) == 1 {
				if next, ok := shellsyntax.LiteralCd(statements[0], directory); ok {
					directory = next
				}
			}
			input := command
			input.Command, input.Workdir = segment.Source, directory
			files, _ := ProjectCaptured(ctx, changecapture.Observation{Files: observation.Files, Commands: []commandanalysis.Command{input}})
			if len(files) > 0 {
				sources = append(sources, segment.Source)
			}
		}
	}
	return sources
}
