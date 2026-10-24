package sessionapi

import (
	"bytes"
	json "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/yusing/goutils/synk"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
)

// Each view retains only its latest immutable feed encodings. Metadata updates
// and bounded-prefix probes can reuse them without revisiting retained entries.
type nativeFeedEncoding struct {
	source *activity.Publication
	data   []byte
}

func (e *nativeFeedEncoding) update(source *activity.Publication) error {
	if e.source == source && e.data != nil {
		return nil
	}
	data, err := json.Marshal(source, nativeEncoding)
	if err != nil {
		return err
	}
	e.source, e.data = source, data
	return nil
}

// Shadow only the feeds, keeping every other NativeFrame field on its owner.
// These omitted fields are appended from validated immutable encodings after
// prefix selection, avoiding even a JSON rescan of unchanged history.
type nativeFrameMetadata struct {
	*NativeFrame
	// The outer pointer distinguishes an omitted cached feed from an explicit
	// null feed. Both encoding paths keep these members last, so identical
	// frames remain byte-identical when a cold cache warms up.
	Main   **activity.Publication `json:"main,omitzero"`
	Agents **activity.Publication `json:"agents,omitzero"`
}

// subscribe admits at most 256 bytes of subscription identity. Each byte can
// require a six-byte JSON escape. Reserve exactly that worst-case envelope,
// including the longer snapshot kind, within the client's 64 MiB read limit.
const nativePublicationBytes = subscriptionBytes - 256*6 - len(`{"jsonrpc":"2.0","method":"subscription/event","params":{"subscription":"","kind":"snapshot","data":}}`)

var errNativePublicationLimit = errors.New("native publication exceeds the 64 MiB transport limit")

// The execution owner retains every unacknowledged consequence. Only its ordered
// prefix travels here; neither a shorter subsequent prefix nor queue replacement
// acknowledges a withheld effect. A receipt for the visible prefix admits the
// suffix on the next publication, even when changing state shrinks the window.
func encodeNativePrefix(attachment, startup string, frame NativeFrame, feeds *[2]nativeFeedEncoding) ([]byte, error) {
	// A newly replaced feed may change again on the next observation. Encode
	// that frame directly; materialize a reusable feed encoding only when its
	// identity survives another changed frame, avoiding duplicate copies on
	// continuously advancing history.
	reuse := true
	for i, feed := range []*activity.Publication{frame.Main, frame.Agents} {
		if feeds[i].source != feed {
			feeds[i] = nativeFeedEncoding{source: feed}
			reuse = false
		}
	}
	feedBytes := 0
	if reuse {
		for i, feed := range []*activity.Publication{frame.Main, frame.Agents} {
			if err := feeds[i].update(feed); err != nil {
				return nil, err
			}
		}
		feedBytes = len(`,"main":,"agents":`) + len(feeds[0].data) + len(feeds[1].data)
	}
	appendFeeds := func(data []byte) []byte {
		if !reuse {
			return data
		}
		// The shadow frame is the final event member and is always an object.
		// Insert its two feed members before the frame and event closing braces.
		pool := synk.GetUnsizedBytesPool()
		encoded := pool.GetAtLeast(len(data) + feedBytes)
		defer func() {
			clear(encoded) // Native consequences may carry retained private input.
			pool.Put(encoded)
		}()
		encoded = append(encoded, data[:len(data)-2]...)
		encoded = append(encoded, `,"main":`...)
		encoded = append(encoded, feeds[0].data...)
		encoded = append(encoded, `,"agents":`...)
		encoded = append(encoded, feeds[1].data...)
		encoded = append(encoded, '}', '}')
		// The queue and last-frame comparison outlive this pooled construction.
		return bytes.Clone(encoded)
	}
	encodeFrame := func(current NativeFrame) ([]byte, error) {
		event := NativeEvent{Attachment: attachment, Startup: startup, Frame: current}
		encoded := struct {
			*NativeEvent
			Frame nativeFrameMetadata `json:"frame"`
		}{NativeEvent: &event, Frame: nativeFrameMetadata{NativeFrame: &event.Frame}}
		if !reuse {
			encoded.Frame.Main, encoded.Frame.Agents = &event.Frame.Main, &event.Frame.Agents
		}
		return json.Marshal(&encoded, nativeEncoding)
	}
	mainOutput, agentOutput := frame.MainOutput, frame.AgentOutput
	frame.MainOutput, frame.AgentOutput = nil, nil
	limit := nativePublicationBytes - feedBytes
	if len(mainOutput)+len(agentOutput) > 0 && !frame.ReacquireOutput {
		limit -= len(`,"reacquireOutput":true`)
	}
	effects := frame.Effects
	project := func(count int) NativeFrame {
		current := frame
		current.Effects = effects[:count]
		if count < len(effects) {
			current.PendingEffects = true
			before := effects[count].ID
			if frame.Activity != nil {
				activity := *frame.Activity
				activity.Reset = min(activity.Reset, before-1)
				activity.Effects = nil
				for _, effect := range frame.Activity.Effects {
					if effect.ID < before {
						activity.Effects = append(activity.Effects, effect)
					}
				}
				current.Activity = &activity
			}
			if current.Notification != nil && current.Notification.ID >= before {
				current.Notification = nil
			}
		}
		return current
	}
	encode := func(count int) ([]byte, error) {
		return encodeFrame(project(count))
	}
	data, err := encode(0)
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, fmt.Errorf("complete state: %w", errNativePublicationLimit)
	}
	// Measure consequences individually so a large retained suffix never requires
	// encoding its aggregate. At most one indivisible effect exceeds the budget.
	upper, bytes := 0, 0
	for _, effect := range effects {
		encoded, err := json.Marshal(effect, nativeEncoding)
		if err != nil {
			return nil, err
		}
		bytes += len(encoded)
		if bytes > nativePublicationBytes {
			break
		}
		upper++
	}
	// Encoded state, gating metadata, separators and effect payloads all count.
	// Their size is monotone as the semantic prefix grows.
	count, high := 0, upper
	if upper > 0 {
		encoded, err := encode(upper)
		if err != nil {
			return nil, err
		}
		if len(encoded) <= limit {
			count, data = upper, encoded
		} else {
			high--
		}
	}
	for count < high {
		candidate := count + (high-count+1)/2
		encoded, err := encode(candidate)
		if err != nil {
			return nil, err
		}
		if len(encoded) <= limit {
			count, data = candidate, encoded
		} else {
			high = candidate - 1
		}
	}
	if len(effects) > 0 && count == 0 {
		return nil, fmt.Errorf("semantic consequence %d cannot fit with the complete state: %w", effects[0].ID, errNativePublicationLimit)
	}
	if len(mainOutput)+len(agentOutput) > 0 {
		current := project(count)
		current.MainOutput, current.AgentOutput = mainOutput, agentOutput
		data, err = encodeFrame(current)
		if err != nil {
			return nil, err
		}
		data = normalizeNativeOutput(publication{Kind: "patch", Data: appendFeeds(data)}).Data
		if len(data) > nativePublicationBytes {
			return nil, fmt.Errorf("indivisible output with complete state: %w", errNativePublicationLimit)
		}
		return data, nil
	}
	return appendFeeds(data), nil
}
