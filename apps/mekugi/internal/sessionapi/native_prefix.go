package sessionapi

import (
	json "encoding/json/v2"
	"errors"
	"fmt"

	activity "github.com/yusing/mekugi/modules/activity/presentation"
	changes "github.com/yusing/mekugi/modules/changehistory/presentation"
)

// Native wire deltas retain immutable owners from the preceding publication.
// They are transport state only; rendering consumers receive complete frames.
type nativeOwners uint8

const (
	nativeMain nativeOwners = 1 << iota
	nativeAgents
	nativeReview
	nativeFamily
)

type nativeWireEvent struct {
	NativeEvent
	Retained nativeOwners `json:"retained,omitzero"`
}

// Owner values are appended from their individual encodings after prefix
// selection. Probing a semantic prefix never re-encodes retained history.
type nativeFrameMetadata struct {
	*NativeFrame
	Main   **activity.Publication `json:"main,omitzero"`
	Agents **activity.Publication `json:"agents,omitzero"`
	Review **changes.ReviewView   `json:"review,omitzero"`
	Family **Family               `json:"family,omitzero"`
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
func encodeNativePrefix(attachment, startup string, frame NativeFrame, retained nativeOwners, sizes *[4]int) ([]byte, error) {
	var owners [4][]byte
	ownerBytes, retainedBytes := 0, 0
	for i, owner := range []any{frame.Main, frame.Agents, frame.Review, frame.Family} {
		if retained&(1<<i) == 0 {
			data, err := json.Marshal(owner, nativeEncoding)
			if err != nil {
				return nil, err
			}
			owners[i], sizes[i] = data, len(data)
		} else {
			retainedBytes += sizes[i]
		}
		ownerBytes += sizes[i]
	}
	// Keep complete state within the reconnect snapshot limit even though a
	// delta itself can be much smaller. Count all owner field names as well.
	ownerBytes += len(`,"main":,"agents":,"review":,"family":`)
	appendOwners := func(data []byte) []byte {
		if retained == nativeMain|nativeAgents|nativeReview|nativeFamily {
			return data
		}
		// This is the final immutable allocation retained by the queue and
		// last-frame comparison, not borrowed scratch that can be returned.
		encoded := make([]byte, 0, len(data)+ownerBytes-retainedBytes)
		encoded = append(encoded, data[:len(data)-2]...)
		for i, field := range []string{`,"main":`, `,"agents":`, `,"review":`, `,"family":`} {
			if owners[i] != nil {
				encoded = append(encoded, field...)
				encoded = append(encoded, owners[i]...)
			}
		}
		encoded = append(encoded, '}', '}')
		return encoded
	}
	encodeFrame := func(current NativeFrame) ([]byte, error) {
		// Frame stays last so appendOwners can splice its validated encodings.
		encoded := struct {
			Attachment string              `json:"attachment"`
			Startup    string              `json:"startup,omitzero"`
			Retained   nativeOwners        `json:"retained,omitzero"`
			Frame      nativeFrameMetadata `json:"frame"`
		}{attachment, startup, retained, nativeFrameMetadata{NativeFrame: &current}}
		return json.Marshal(&encoded, nativeEncoding)
	}
	mainOutput, agentOutput := frame.MainOutput, frame.AgentOutput
	frame.MainOutput, frame.AgentOutput = nil, nil
	limit := nativePublicationBytes - ownerBytes
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
		data = normalizeNativeOutputLimit(publication{Kind: "patch", Data: appendOwners(data)}, nativePublicationBytes-retainedBytes).Data
		if len(data) > nativePublicationBytes-retainedBytes {
			return nil, fmt.Errorf("indivisible output with complete state: %w", errNativePublicationLimit)
		}
		return data, nil
	}
	return appendOwners(data), nil
}
