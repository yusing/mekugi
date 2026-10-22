package sessionapi

import (
	json "encoding/json/v2"
	"errors"
	"fmt"
)

// subscribe admits at most 256 bytes of subscription identity. Each byte can
// require a six-byte JSON escape. Reserve exactly that worst-case envelope,
// including the longer snapshot kind, within the client's 64 MiB read limit.
const nativePublicationBytes = subscriptionBytes - 256*6 - len(`{"jsonrpc":"2.0","method":"subscription/event","params":{"subscription":"","kind":"snapshot","data":}}`)

var errNativePublicationLimit = errors.New("native publication exceeds the 64 MiB transport limit")

// The execution owner retains every unacknowledged consequence. Only its ordered
// prefix travels here; neither a shorter subsequent prefix nor queue replacement
// acknowledges a withheld effect. A receipt for the visible prefix admits the
// suffix on the next publication, even when changing state shrinks the window.
func encodeNativePrefix(attachment, startup string, frame NativeFrame) ([]byte, error) {
	mainOutput, agentOutput := frame.MainOutput, frame.AgentOutput
	frame.MainOutput, frame.AgentOutput = nil, nil
	limit := nativePublicationBytes
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
		return json.Marshal(NativeEvent{Attachment: attachment, Startup: startup, Frame: project(count)}, nativeEncoding)
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
		data, err = json.Marshal(NativeEvent{Attachment: attachment, Startup: startup, Frame: current}, nativeEncoding)
		if err != nil {
			return nil, err
		}
		data = normalizeNativeOutput(publication{Kind: "patch", Data: data}).Data
		if len(data) > nativePublicationBytes {
			return nil, fmt.Errorf("indivisible output with complete state: %w", errNativePublicationLimit)
		}
	}
	return data, nil
}
