package router

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/yusing/mekugi"
	"mvdan.cc/sh/v3/interp"
)

// Copy into an absent private destination first. Source inodes remain untouched
// until the worker commits the destination replacement; failed installs need no
// copy-back rollback that would silently break source hard links/open handles.
func (t *shellFileTracker) crossDeviceMove(ctx context.Context, source, target, sourceReview, targetReview, old string, exists bool, names []string, files *[]mekugi.ReviewFile, captureErr error) error {
	parent, _ := filepath.Split(target)
	staging, err := os.MkdirTemp(parent, ".mekugi-move-")
	if err != nil {
		return err
	}
	staged := filepath.Join(staging, "entry")
	handler := interp.HandlerCtx(ctx)
	cleanup := func(cause error) error {
		// This directory contains only our copies, never the source inodes.
		if err := os.RemoveAll(staging); err != nil {
			return errors.Join(cause, fmt.Errorf("temporary copy retained at %s: %w", staging, err))
		}
		return cause
	}
	if err := runExternalShellCommand(ctx, []string{"cp", "-a", "--", source, staged}, false, handler); err != nil {
		return cleanup(err)
	}
	if err := ctx.Err(); err != nil {
		return cleanup(err)
	}
	if err := os.Rename(staged, target); err != nil {
		return cleanup(err)
	}
	if exists {
		*files = append(*files, shellOperationReview(targetReview, "", old, "", captureErr))
	}
	var failures []error
	// Children precede parents. If unlink fails, record a copy rather than
	// inventing a move; the installed destination remains usable.
	for _, name := range slices.Backward(names) {
		if err := os.Remove(source + name); err == nil {
			*files = append(*files, mekugi.RenderReviewFile(sourceReview+name, targetReview+name, "", ""))
		} else {
			failures = append(failures, err)
			info, captureErr := os.Lstat(target + name)
			var content string
			if captureErr == nil {
				content, captureErr = shellRemovedContent(target+name, info)
			}
			*files = append(*files, shellOperationReview("", targetReview+name, "", content, captureErr))
		}
	}
	if err := cleanup(nil); err != nil {
		// Auxiliary cleanup must not hide the committed operation's evidence.
		fmt.Fprintln(handler.Stderr, err)
	}
	return errors.Join(failures...)
}
