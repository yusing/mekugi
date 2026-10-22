package launch

import (
	"context"
	"crypto/rand"
	"errors"
	"image"
	"io"
	"os"
	"path/filepath"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	"github.com/yusing/mekugi/libraries/sourceanalysis/sourceparse"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

// prepareNativeDraft borrows client source paths. Acquired copies survive the
// client's editor lifetime only after the execution owner admits the request.
func prepareNativeDraft(ctx context.Context, workspace string, source composer.Draft) (_ *sessionapi.PreparedInput, err error) {
	invalid := func(message string) error { return &sessionapi.InputError{Message: message} }
	if !filepath.IsAbs(workspace) {
		return nil, invalid("Native input requires the selected thread's absolute workspace.")
	}
	if source.CursorBack < 0 || source.CursorBack > len(source.Text) {
		return nil, invalid("The native draft has an invalid cursor.")
	}
	end := 0
	for _, span := range source.Spans() {
		if span.Start < end || span.End <= span.Start || span.End > len(source.Text) {
			return nil, invalid("The native draft has invalid attachment spans.")
		}
		end = span.End
	}
	draft := source.Clone()
	for _, snapshot := range draft.Attachments {
		if _, ok := attachments.Decode(snapshot); !ok {
			return nil, invalid("The native draft has an invalid text snapshot.")
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(draft.Attachments) == 0 {
		draft.SnapshotFiles(workspace)
	}
	var acquired []string
	defer func() {
		if err != nil {
			for _, path := range acquired {
				_ = os.Remove(path)
			}
		}
	}()
	images := make(map[string]string)
	acquire := func(path string) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if path == "" {
			return "", invalid("An image attachment requires a source path.")
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(workspace, path)
		}
		path = filepath.Clean(path)
		if copy := images[path]; copy != "" {
			return copy, nil
		}
		copy, err := snapshotNativeImage(ctx, path)
		if err == nil {
			images[path] = copy
			acquired = append(acquired, copy)
		}
		return copy, err
	}
	for i := range draft.Images {
		if draft.Images[i].Path, err = acquire(draft.Images[i].Path); err != nil {
			return nil, err
		}
	}
	for i := range draft.AnswerImages {
		if draft.AnswerImages[i], err = acquire(draft.AnswerImages[i]); err != nil {
			return nil, err
		}
	}
	validation := draft.Clone()
	if _, err := prepareSubmissionInput(&validation, rand.Text()); err != nil {
		return nil, &sessionapi.InputError{Message: err.Error(), Limit: true}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return sessionapi.NewPreparedInput(draft, acquired), nil
}

func snapshotNativeImage(ctx context.Context, path string) (_ string, err error) {
	source, err := sourceparse.Open(path)
	if err != nil {
		return "", &sessionapi.InputError{Message: "The native image source could not be opened."}
	}
	defer source.Close()
	stop := context.AfterFunc(ctx, func() { _ = source.Close() })
	defer stop()
	info, err := source.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", &sessionapi.InputError{Message: "A native image source must be a regular file."}
	}
	_, format, err := image.DecodeConfig(source)
	if err != nil {
		return "", &sessionapi.InputError{Message: "The native image source is not a supported image."}
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	file, err := os.CreateTemp("", "mekugi-upload-*."+format)
	if err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(file.Name())
		}
	}()
	_, err = io.Copy(file, source)
	err = errors.Join(err, file.Close(), ctx.Err())
	if err != nil {
		return "", err
	}
	return file.Name(), nil
}
