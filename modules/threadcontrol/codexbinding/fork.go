package codexbinding

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"slices"

	"github.com/yusing/mekugi/backends/codex/binding"
)

type forkObject = map[string]jsontext.Value

type forkRecord struct {
	Type     string     `json:"type"`
	Payload  forkObject `json:"payload"`
	Metadata forkObject `json:"metadata,omitzero"`
	Other    forkObject `json:",embed"`
}

// Source: codex-rs/core/src/agent/control/spawn.rs:1098:1326@0df4aa677
// V2 sanitation. Conversation items are retained without stock fork filtering;
// only generated identity and non-transferable authority are sanitized.
func sanitizeFork(prefix []byte) ([]byte, error) {
	var records []forkRecord
	for line := range bytes.SplitSeq(bytes.TrimSpace(prefix), []byte{'\n'}) {
		var record forkRecord
		if err := json.Unmarshal(line, &record); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	baselines := true
	for _, record := range slices.Backward(records) {
		if record.Type == "compacted" {
			baselines = record.Payload["replacement_history"].Kind() == '['
			break
		}
	}
	var output bytes.Buffer
	encoder := jsontext.NewEncoder(&output)
	for _, record := range records {
		payload := record.Payload
		switch record.Type {
		case "response_item":
			keep, metadata, err := sanitizeForkItem(payload, record.Metadata, baselines)
			if err != nil {
				return nil, err
			}
			if !keep {
				continue
			}
			record.Metadata = metadata
		case "compacted":
			delete(payload, "latest_token_usage_record")
			delete(payload, "guardian_history")
			for _, field := range []string{"retained_context", "resume_metadata"} {
				if payload[field].Kind() != '{' {
					continue
				}
				var context forkObject
				if err := json.Unmarshal(payload[field], &context); err != nil {
					return nil, err
				}
				if field == "retained_context" {
					clearForkGrants(context)
				} else {
					context["multi_agent_version"] = jsontext.Value(`"v2"`)
					delete(context, "turn_attribution")
					if !baselines {
						delete(context, "previous_turn_settings")
					}
				}
				payload[field], _ = json.Marshal(context)
			}
			if payload["replacement_history"].Kind() == '[' {
				var replacement struct {
					Items    []forkObject `json:"replacement_history"`
					Metadata []forkObject `json:"replacement_history_metadata"`
				}
				raw, _ := json.Marshal(payload)
				if err := json.Unmarshal(raw, &replacement); err != nil {
					return nil, err
				}
				if replacement.Metadata == nil {
					replacement.Metadata = make([]forkObject, len(replacement.Items))
				}
				if len(replacement.Metadata) != len(replacement.Items) {
					return nil, errors.New("fork compaction metadata does not match its replacement history")
				}
				var kept, metas []forkObject
				for i, item := range replacement.Items {
					keep, meta, err := sanitizeForkItem(item, replacement.Metadata[i], baselines)
					if err != nil {
						return nil, err
					}
					if keep {
						kept, metas = append(kept, item), append(metas, meta)
					}
				}
				payload["replacement_history"], _ = json.Marshal(kept)
				payload["replacement_history_metadata"], _ = json.Marshal(metas)
			}
		case "world_state":
			if !baselines {
				continue
			}
			var state forkObject
			if err := json.Unmarshal(payload["state"], &state); err != nil {
				return nil, err
			}
			delete(state, "multi_agent_usage_hint")
			payload["state"], _ = json.Marshal(state)
		case "turn_context":
			if !baselines {
				continue
			}
		case "retained_context":
			clearForkGrants(payload)
		case "event_msg", "session_meta":
		default:
			continue
		}
		if err := json.MarshalEncode(encoder, &record); err != nil {
			return nil, err
		}
	}
	return output.Bytes(), nil
}

func clearForkGrants(authority forkObject) {
	// Independent runtimes retain genuine source context, not the native V2
	// known-empty reset that relies on a separate live root registry.
	authority["verified_answers"] = jsontext.Value(`[]`)
	authority["incomplete"] = jsontext.Value(`true`)
}

func sanitizeForkItem(item, metadata forkObject, baselines bool) (bool, forkObject, error) {
	kind, role := forkString(item, "type"), forkString(item, "role")
	if kind == "additional_tools" && !baselines {
		return false, nil, nil
	}
	if role == "developer" {
		var developer struct {
			Content []jsontext.Value `json:"content"`
			Native  struct {
				Kinds []*string  `json:"content_item_kinds"`
				Other forkObject `json:",embed"`
			} `json:"internal_chat_message_metadata_passthrough"`
		}
		raw, _ := json.Marshal(item)
		if err := json.Unmarshal(raw, &developer); err != nil {
			return false, nil, err
		}
		if developer.Native.Kinds != nil {
			if len(developer.Native.Kinds) != len(developer.Content) {
				return false, nil, errors.New("fork instruction provenance has mismatched content indices")
			}
			var kept []jsontext.Value
			var labels []*string
			for i, part := range developer.Content {
				label := ""
				if developer.Native.Kinds[i] != nil {
					label = *developer.Native.Kinds[i]
				}
				drop := slices.Contains([]string{"multi_agent.role_instructions", "multi_agent.usage_hint", "multi_agent.mode_instructions", "guardian.approved_action"}, label)
				drop = drop || !baselines && slices.Contains([]string{"model.base_instructions", "tools.incremental_update", "managed_config.developer_instructions", "model.instructions", "persistent_mode.instructions"}, label)
				if !drop {
					kept, labels = append(kept, part), append(labels, developer.Native.Kinds[i])
				}
			}
			if len(kept) == 0 {
				return false, nil, nil
			}
			item["content"], _ = json.Marshal(kept)
			developer.Native.Kinds = labels
			item["internal_chat_message_metadata_passthrough"], _ = json.Marshal(developer.Native)
		}
	}
	if metadata == nil {
		metadata = make(forkObject)
	}
	metadata["inherited_user_message"] = jsontext.Value(`true`)
	sender := metadata["sender_user_messages"]
	if sender.Kind() != 0 && sender.Kind() != 'n' || kind != "message" || role != "user" {
		delete(metadata, "user_input_order")
	}
	delete(metadata, "sender_user_messages")
	return true, metadata, nil
}

func forkString(object forkObject, field string) string {
	var value string
	_ = json.Unmarshal(object[field], &value)
	return value
}

func (d *Driver) forkSource(ctx context.Context, caller, thread string) (binding.ForkSource, error) {
	owner, err := d.runtime(caller)
	if err != nil {
		return binding.ForkSource{}, err
	}
	info, err := owner.Thread(ctx, thread)
	if err != nil {
		return binding.ForkSource{}, err
	}
	if info.Thread != thread || info.Path == "" {
		return binding.ForkSource{}, errors.New("fork source has no confirmed host history")
	}
	return binding.ForkSource{Thread: thread, Rollout: thread, Path: info.Path}, nil
}
