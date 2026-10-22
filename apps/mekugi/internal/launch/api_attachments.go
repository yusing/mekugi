package launch

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	"github.com/yusing/mekugi/libraries/pathdisplay"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

// prepareAPIInput borrows host paths and snapshots text before admission. It
// never touches the session's editor, picker, queue or request ledger.
func prepareAPIInput(ctx context.Context, workspace string, input sessionapi.InputRequest) (_ *sessionapi.PreparedInput, err error) {
	if input.NativeDraft != nil {
		return prepareNativeDraft(ctx, workspace, *input.NativeDraft)
	}
	invalid := func(message string, limit bool) error {
		return &sessionapi.InputError{Message: message, Limit: limit}
	}
	metadata := len(input.Text)
	for _, attachment := range input.Attachments {
		metadata += len(attachment.Source) + len(attachment.Path) + len(attachment.Name)
	}
	if metadata > responses.UserTextLimit {
		return nil, invalid("Input text and attachment metadata exceed the 1 MiB limit.", true)
	}
	if !filepath.IsAbs(workspace) {
		return nil, invalid("Attachment input requires the selected thread's absolute workspace.", false)
	}
	var draft composer.Draft
	var text strings.Builder
	text.WriteString(input.Text)
	var sources []composer.AttachmentSource
	var acquired []string
	defer func() {
		if err != nil {
			for _, path := range acquired {
				_ = os.Remove(path)
			}
		}
	}()
	total := 0
	for _, attachment := range input.Attachments {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var path, label string
		var upload composer.Token
		isImage := false
		switch attachment.Source {
		case "path":
			if attachment.Path == "" || attachment.Name != "" || attachment.Data != nil {
				return nil, invalid("A path attachment requires only source and path.", false)
			}
			path = attachment.Path
			if !filepath.IsAbs(path) {
				path = filepath.Join(workspace, path)
			}
			path = filepath.Clean(path)
			label = "@" + pathdisplay.ForWorkspace(workspace, path)
			isImage = imageFile(path)
			if !isImage {
				sources = append(sources, composer.AttachmentSource{Path: path})
			}
		case "upload":
			if strings.TrimSpace(attachment.Name) == "" || attachment.Path != "" || attachment.Data == nil {
				return nil, invalid("An upload requires a display name and base64 data, without a host path.", false)
			}
			// Check the encoded bound before allocating the decoded byte slice.
			if len(*attachment.Data) > base64.StdEncoding.EncodedLen(sessionapi.UploadLimit-total) {
				return nil, invalid("Combined uploads exceed the 32 MiB decoded limit.", true)
			}
			data, decodeErr := base64.StdEncoding.Strict().DecodeString(*attachment.Data)
			if decodeErr != nil {
				return nil, invalid("Upload data is not valid base64.", false)
			}
			total += len(data)
			if total > sessionapi.UploadLimit {
				return nil, invalid("Combined uploads exceed the 32 MiB decoded limit.", true)
			}
			_, format, imageErr := image.DecodeConfig(bytes.NewReader(data))
			isImage = imageErr == nil
			if isImage {
				file, createErr := os.CreateTemp("", "mekugi-upload-*."+format)
				if createErr != nil {
					return nil, createErr
				}
				path = file.Name()
				acquired = append(acquired, path)
				_, writeErr := file.Write(data)
				if writeErr = errors.Join(writeErr, file.Close()); writeErr != nil {
					return nil, writeErr
				}
			} else {
				sources = append(sources, composer.AttachmentSource{Name: attachment.Name, Data: data})
				upload = composer.UploadAttachment(attachment.Name, data)
			}
			label = "[Uploaded " + attachment.Name + "]"
		default:
			return nil, invalid("Attachment source must be path or upload.", false)
		}
		if text.Len() > 0 {
			text.WriteByte('\n')
		}
		if isImage {
			label = fmt.Sprintf("[Image %d]", len(draft.Images)+1)
		}
		start := text.Len()
		text.WriteString(label)
		token := composer.Token{Start: start, End: text.Len(), Path: path}
		if isImage {
			draft.Images = append(draft.Images, token)
		} else {
			if attachment.Source == "upload" {
				token.Name, token.Text, token.Source = upload.Name, upload.Text, upload.Source
			}
			draft.Files = append(draft.Files, token)
		}
	}
	draft.Text = text.String()
	draft.SnapshotSources(workspace, sources)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, snapshot := range draft.Attachments {
		if _, ok := attachments.Decode(snapshot); !ok {
			return nil, invalid("Attachment exceeds the supported text envelope limits.", true)
		}
	}
	validation := draft.Clone()
	if _, err := prepareSubmissionInput(&validation, rand.Text()); err != nil {
		return nil, invalid(err.Error(), true)
	}
	if strings.TrimSpace(draft.Text) == "" {
		return nil, invalid("Input is empty.", false)
	}
	return sessionapi.NewPreparedInput(draft, acquired), nil
}
