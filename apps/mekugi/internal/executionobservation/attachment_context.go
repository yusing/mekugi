package executionobservation

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"strings"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/commandargs"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

type attachmentPreparation struct{ snapshot composer.AttachmentSnapshot }

func attachmentRecallHint(groups []string) string {
	return "Optional attachment recall (read with mcat): " + strings.Join(groups, "; ")
}

// Publish optional retained selection, source-placeholder and image recall.
func (p *Preparer) prepareAttachmentHints(ctx context.Context, request *codexresponses.Request, prepared attachmentPreparation) error {
	snapshot := prepared.snapshot
	if snapshot.Recall == nil {
		return nil
	}
	hint, err := snapshot.Recall.Publish("hint", func() (string, error) {
		var selections, files, images, skills []string
		for _, value := range snapshot.Values {
			if value.SelectionText != "" {
				selections = append(selections, value.SelectionText)
			}
			if len(value.Selection.Frames) > 0 {
				selections = append(selections, value.Selection.Frames...)
			}
			for _, label := range value.Placeholders {
				if strings.HasPrefix(label, "$") {
					skills = append(skills, label)
				} else if !composer.SelectionLabel(label) && !strings.HasPrefix(label, "[Uploaded ") {
					files = append(files, label)
				}
			}
			for _, image := range value.Images {
				images = append(images, fmt.Sprintf("%s source: %s", image.Name, image.Path))
			}
		}
		if len(selections)+len(files)+len(images)+len(skills) == 0 {
			return "", nil
		}
		selected := observationScope(ctx, p.Retained)
		if selected.Store == nil {
			return "", errors.New("attachment recall storage is unavailable")
		}
		var hints []string
		for _, group := range []struct {
			kind   string
			values []string
		}{{"selections", selections}, {"skills", skills}, {"files", files}, {"images", images}} {
			if len(group.values) == 0 {
				continue
			}
			path, _, err := selected.Store.WriteReadOnlyFile(ctx, selected.Thread, selected.Limits, selected.Writes, group.kind, ".txt", []byte(strings.Join(group.values, "\n")))
			if err != nil {
				return "", err
			}
			hints = append(hints, group.kind+": "+commandargs.QuoteArgument(path))
		}
		return attachmentRecallHint(hints), nil
	})
	if err != nil || hint == "" {
		return err
	}
	var items []map[string]jsontext.Value
	if err := json.Unmarshal(request.Fields["input"], &items); err != nil {
		return err
	}
	item := map[string]jsontext.Value{"type": responses.MustMarshalJSON("message"), "role": responses.MustMarshalJSON("user"), "content": responses.MustMarshalJSON([]map[string]string{{"type": "input_text", "text": hint}})}
	// A stable window-start anchor lets every later native assistant/tool item
	// extend the exact provider-confirmed prefix, including explicit reloads.
	request.Fields["input"] = responses.MustMarshalJSON(append([]map[string]jsontext.Value{item}, items...))
	return nil
}

func (p *Preparer) prepareAttachmentContext(ctx context.Context, request *codexresponses.Request, thread string, metadata codexresponses.TurnMetadata, prewarm bool) (attachmentPreparation, error) {
	if p.ContextAttachments == nil || prewarm || metadata.RequestKind != responses.Turn || metadata.WindowNumber == 0 || metadata.ContextWindowID == "" {
		return attachmentPreparation{}, nil
	}
	snapshot, err := p.ContextAttachments(ctx, thread, metadata.ContextWindowID)
	if err != nil {
		return attachmentPreparation{}, err
	}
	values := snapshot.Values
	if len(values) == 0 {
		return attachmentPreparation{}, nil
	}
	if err := requestexecution.OmitCompactedAttachments(request, values); err != nil {
		return attachmentPreparation{}, err
	}
	return attachmentPreparation{snapshot: snapshot}, nil
}
