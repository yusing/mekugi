package sessionpresentation

import (
	"strings"
	"time"

	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/libraries/pathdisplay"
	"github.com/yusing/mekugi/libraries/textsanitize"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
)

// NativeSourceAcquisitionEntry shares the actual reader receipt across live,
// restored and replayed Activity, without parsing context bodies or shell output.
func NativeSourceAcquisitionEntry(cwd, thread, turn string, receipt *observation.SourceAcquisition, at time.Time) (ActivityEntry, bool) {
	if receipt == nil || receipt.ID == "" || len(receipt.Sources) == 0 || receipt.Purpose != "preload" && receipt.Purpose != "reload" {
		return ActivityEntry{}, false
	}
	values := make([]activity.Value, 0, len(receipt.Sources))
	for _, source := range receipt.Sources {
		value := activity.Value{Kind: "op", Verb: "Attached", AttachmentKind: "file", AutoReload: receipt.Purpose == "reload", Exited: true, Segment: true}
		switch source.Kind {
		case "file":
			value.Path = pathdisplay.ForWorkspace(cwd, source.Path)
		case "skill":
			value.Verb, value.AttachmentKind, value.Label = "Attached skill", "skill", source.Name
		case "changes":
			value.Verb, value.AttachmentKind, value.Label = "Attached changes", "change", strings.Join(source.Selection, ",")
		default:
			value.Verb, value.AttachmentKind, value.Label = "Attach failed", "selection", source.Reference
		}
		if !source.Success {
			value.ExitCode = 1
		}
		if source.Diagnostic != "" {
			if value.Label != "" {
				value.Label += " · "
			}
			value.Label += source.Diagnostic
		}
		value.Label = textsanitize.Safe(value.Label, false)
		values = append(values, value)
	}
	return ActivityEntry{Agent: "/root", Kind: "attachments", Observed: at, Completed: at,
		Native: &NativeItem{Thread: thread, Turn: turn, Item: "native:sources:" + receipt.ID, Phase: "item/completed", Attachments: values}}, true
}
