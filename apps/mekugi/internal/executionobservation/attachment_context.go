package executionobservation

import (
	"bytes"
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
	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
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

func attachmentRecallHint(groups []string) string {
	return "Optional attachment recall: " + strings.Join(groups, "; ")
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
			sources, err := captured.SelectedSources()
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

func (p *Preparer) prepareAttachmentContext(ctx context.Context, request *codexresponses.Request, thread string, metadata codexresponses.TurnMetadata, prewarm bool, historical []compact.SourceIdentity) (attachmentPreparation, error) {
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
	selected := make(map[string]bool)
	for _, source := range historical {
		if len(source.Ranges) == 0 && source.Changes == nil {
			selected[source.Skill+"\x00"+source.Path] = true
		}
	}
	explicit := maps.Clone(selected)
	paths, names := nativeAttachmentSources(request.Fields["input"])
	var sources []compact.SourceIdentity
	for _, value := range values {
		for _, skill := range value.Skills {
			key := skill.Name + "\x00" + skill.Path
			if paths[skill.Path] || names[skill.Name] {
				selected[key], explicit[key] = true, true
			}
			if selected[key] {
				continue
			}
			selected[key] = true
			sources = append(sources, compact.SourceIdentity{Skill: skill.Name, Path: skill.Path})
		}
	}
	selection, err := snapshot.Recall.Publish("reader-selection", func() (string, error) {
		if len(sources) == 0 {
			return "", nil
		}
		data, err := json.Marshal(struct {
			Sources []compact.SourceIdentity `json:"sources"`
		}{sources})
		return string(data), err
	})
	if err != nil {
		return attachmentPreparation{}, err
	}
	if selection == "" {
		return attachmentPreparation{snapshot: snapshot}, nil
	}

	var items []map[string]jsontext.Value
	if err := json.Unmarshal(request.Fields["input"], &items); err != nil {
		return attachmentPreparation{}, err
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

// The native prompt supplies this receipt from the retained reload envelope.
// Only complete, successful source identities retire optional skill acquisition;
// source prose and byte equality cannot establish that a skill was selected.
func nativeAttachmentSources(input jsontext.Value) (paths, names map[string]bool) {
	if !bytes.Contains(input, []byte(`"source_acquisition"`)) {
		return nil, nil
	}
	var items []struct {
		Type      string         `json:"type"`
		Name      string         `json:"name"`
		Namespace string         `json:"namespace"`
		CallID    jsontext.Value `json:"call_id"`
		Metadata  struct {
			SourceAcquisition *observation.SourceAcquisition `json:"source_acquisition"`
		} `json:"internal_chat_message_metadata_passthrough"`
	}
	if json.Unmarshal(input, &items) != nil {
		return nil, nil
	}
	for _, item := range items {
		receipt := item.Metadata.SourceAcquisition
		if item.Type != "function_call_output" || item.Namespace != "context" || item.Name != "reload" || len(item.CallID) != 0 && !bytes.Equal(item.CallID, []byte("null")) || receipt == nil || receipt.ID == "" || receipt.Purpose != "preload" && receipt.Purpose != "reload" {
			continue
		}
		for _, source := range receipt.Sources {
			if !source.Success {
				continue
			}
			ref, err := attachments.ParseReference(source.Reference)
			if err != nil || len(ref.Ranges) != 0 {
				continue
			}
			switch source.Kind {
			case "file":
				if ref.Path != "" && source.Path != "" {
					if paths == nil {
						paths = make(map[string]bool)
					}
					paths[source.Path] = true
				}
			case "skill":
				if ref.Skill != "" && ref.Skill == source.Name {
					if names == nil {
						names = make(map[string]bool)
					}
					names[source.Name] = true
				}
			}
		}
	}
	return paths, names
}
