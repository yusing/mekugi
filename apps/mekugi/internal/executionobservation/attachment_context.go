package executionobservation

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/modules/agentguidance/compact"
	"github.com/yusing/mekugi/modules/conversation/composer"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
)

type attachmentPreparation struct {
	reader   jsontext.Value
	calls    map[string]bool
	snapshot composer.AttachmentSnapshot
	skills   *compact.Snapshot
	explicit map[string]bool
}

// Freeze the complete hint before the window's first provider continuation.
// Native skill acquisition remains stock-owned; source-reader turns defer it.
func (p *Preparer) prepareAttachmentHints(ctx context.Context, request *codexresponses.Request, prepared attachmentPreparation) error {
	snapshot := prepared.snapshot
	if snapshot.Recall == nil {
		return nil
	}
	hint, err := snapshot.Recall.Publish("hint", func() (string, error) {
		var selections, files, images, skills []string
		for _, value := range snapshot.Values {
			if len(value.Selection.Frames) > 0 {
				selections = append(selections, value.Selection.Frames...)
			}
			for _, label := range value.Placeholders {
				if strings.HasPrefix(label, "$") {
					skills = append(skills, label)
				} else if !composer.SelectionLabel(label) {
					files = append(files, label)
				}
			}
			for _, image := range value.Images {
				images = append(images, fmt.Sprintf("%s source: %s", image.Name, image.Path))
			}
		}
		if captured := prepared.skills; captured != nil {
			sources, err := compact.Sources(captured.Selection)
			if err != nil {
				return "", err
			}
			for i, source := range sources {
				if prepared.explicit[source.Skill+"\x00"+source.Path] {
					continue
				}
				draft := composer.Draft{Skills: []composer.Token{{Name: source.Skill, Path: source.Path}}}
				draft.SnapshotSkills(func(_, _ string) ([]byte, error) {
					value := captured.Sources[i]
					if value.ExitCode != 0 {
						return nil, errors.New(value.Text())
					}
					return []byte(value.Stdout), nil
				})
				skills = append(skills, draft.Attachments...)
			}
		}
		if len(selections)+len(files)+len(images)+len(skills) == 0 {
			return "", nil
		}
		selected := observationScope(ctx, p.Retained)
		store, err := p.Recovery.AttachStore(selected.Store, selected.Namespace, selected.Thread, recovery.DefaultLimits(), selected.Writes)
		if err != nil {
			return "", err
		}
		var hints []string
		for _, group := range []struct {
			kind   string
			values []string
		}{{"selections", selections}, {"skills", skills}, {"files", files}, {"images", images}} {
			if len(group.values) == 0 {
				continue
			}
			ref, err := store.Publish(ctx, command.OmittedOutput{Stdout: strings.Join(group.values, "\n")}, 0)
			if err != nil {
				return "", err
			}
			hints = append(hints, group.kind+": mread "+ref)
		}
		return "Optional attachment recall: " + strings.Join(hints, "; "), nil
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

func (p *Preparer) prepareAttachmentContext(ctx context.Context, request *codexresponses.Request, thread string, metadata codexresponses.TurnMetadata, prewarm bool, historical []compact.Source) (attachmentPreparation, error) {
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
	var items []map[string]jsontext.Value
	if err := json.Unmarshal(request.Fields["input"], &items); err != nil {
		return attachmentPreparation{}, err
	}
	// A caller-selected whole skill already uses the shared current-source reader.
	// Keep its result and failure instead of scheduling an identical second read.
	selections, _, err := preloadSelections(items)
	if err != nil {
		return attachmentPreparation{}, err
	}
	selected := make(map[string]bool)
	for _, source := range historical {
		if len(source.Ranges) == 0 && source.Changes == nil {
			selected[source.Skill+"\x00"+source.Path] = true
		}
	}
	for _, preload := range selections {
		sources, err := compact.ReloadSources(preload.Selection)
		if err != nil {
			return attachmentPreparation{}, err
		}
		for _, source := range sources {
			if len(source.Ranges) == 0 && source.Changes == nil {
				selected[source.Skill+"\x00"+source.Path] = true
			}
		}
	}
	explicit := maps.Clone(selected)
	var sources []compact.Source
	for _, value := range values {
		for _, skill := range value.Skills {
			key := skill.Name + "\x00" + skill.Path
			if selected[key] {
				continue
			}
			selected[key] = true
			sources = append(sources, compact.Source{Skill: skill.Name, Path: skill.Path})
		}
	}
	selection, err := snapshot.Recall.Publish("reader-selection", func() (string, error) {
		if len(sources) == 0 {
			return "", nil
		}
		data, err := json.Marshal(struct {
			Sources []compact.Source `json:"sources"`
		}{sources})
		return string(data), err
	})
	if err != nil {
		return attachmentPreparation{}, err
	}
	if selection == "" {
		return attachmentPreparation{snapshot: snapshot}, nil
	}

	call, readers, err := compactReaderCall(compact.Record{Reference: "attachments:" + metadata.ContextWindowID, Selection: selection}, items, thread)
	calls := make(map[string]bool, len(readers))
	for _, id := range readers {
		calls[id] = true
	}
	if codexresponses.ExecutionFree(request) {
		return attachmentPreparation{calls: calls, snapshot: snapshot}, nil
	}
	return attachmentPreparation{reader: call, calls: calls, snapshot: snapshot, explicit: explicit}, err
}
