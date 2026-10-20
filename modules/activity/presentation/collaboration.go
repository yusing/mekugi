package presentation

import (
	"cmp"
	"strings"

	"github.com/yusing/mekugi/contracts/observation"
)

type CollaborationOrigin uint8

const (
	ObservedCollaboration CollaborationOrigin = iota
	HistoricalCollaboration
)

// CollaborationDelivery is one admitted completion's per-recipient fact.
// From and To remain host thread identities until the application resolves labels.
// IDs retain item/recipient identity independently of feed sequence and rendering.
type CollaborationDelivery struct {
	Kind, ID, AssignmentID string
	From, To, Text         string
	Model, Effort          string
}

// ProjectCollaboration interprets an item whose completion has been admitted.
// Confirmed also admits its participants when a blank body or a lifecycle tool
// produces no feed entry. Unfinished history remains visibly unconfirmed.
// Source: apps/mekugi/internal/router/app_server_events.go:506:542 and
// app_server_restore_content.go:269:280@[50909e84].
func ProjectCollaboration(item observation.Item, origin CollaborationOrigin) ([]CollaborationDelivery, bool) {
	confirmed := item.Status == "completed" || origin == ObservedCollaboration && item.Status == ""
	if !confirmed && origin == ObservedCollaboration {
		return nil, false
	}
	var deliveries []CollaborationDelivery
	for _, receiver := range item.ReceiverThreadIDs {
		d := CollaborationDelivery{ID: item.ID + "\x00" + receiver, From: item.SenderThreadID, To: receiver, Text: item.Prompt}
		switch {
		case !confirmed:
			d.Kind, d.Text = "error", "Historical "+item.Tool+": "+cmp.Or(item.Status, "unknown status")+"; delivery not confirmed"
		case item.Tool == "spawnAgent":
			d.Kind, d.Model, d.Effort = "start", item.Model, item.ReasoningEffort
			if strings.TrimSpace(item.Prompt) != "" {
				d.AssignmentID = item.ID
			}
		case (item.Tool == "sendInput" || item.Tool == "followupTask") && strings.TrimSpace(item.Prompt) != "":
			d.Kind, d.AssignmentID = "assignment", d.ID
		case item.Tool == "sendMessage" && strings.TrimSpace(item.Prompt) != "":
			d.Kind = "reply"
		default:
			continue
		}
		deliveries = append(deliveries, d)
	}
	return deliveries, confirmed
}
