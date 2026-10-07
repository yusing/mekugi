package router

import (
	json "encoding/json/v2"
	"strings"

	"github.com/yusing/mekugi/internal/pathdisplay"
	"github.com/yusing/mekugi/internal/session"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

// Native events supply presentation data to the same operation parser, output
// retention and conversation grouping as the other backend. They are not RPCs.
func (u *appServerUI) runtimeToolEntry(v *liveActivityView, entry activityPaneEntry, e session.Event) activityPaneEntry {
	entry.Kind = "tool"
	entry.native = &liveActivityNativeItem{thread: u.thread, item: e.ID, running: e.Kind == "tool" && !e.Historical}
	entry.Text = e.Role
	for _, old := range v.entries {
		if old.CallID == e.ID && e.ID != "" {
			entry.Text = old.Text
			entry.outputTail, entry.outputOmit = old.outputTail, old.outputOmit
			if old.native != nil {
				*entry.native = *old.native
			}
			break
		}
	}
	if e.Kind == "tool" {
		entry.Text, entry.native.command = runtimeToolText(e.Role, e.Text, u.session.cwd)
		if !e.Historical && entry.native.commandStarted.IsZero() {
			entry.native.commandStarted = entry.Observed
		}
		if entry.native.output == nil {
			entry.native.output = u.session.outputs.New()
		}
		return entry
	}
	if e.Output != nil && e.Output.TaskID != "" && !e.Output.Done && !e.Failed {
		// A native background launch is not command completion. Its placeholder
		// is not stdout and must not replace the live tail or settle its dialog.
		entry.native.background = true
		entry.native.running = !runtimeTaskTerminal(u.runtime.tasks[e.Output.TaskID].Status) && (entry.native.output == nil || !entry.native.output.View().Done)
		if entry.native.output == nil {
			entry.native.output = u.session.outputs.New()
		}
		if !entry.native.running {
			entry.native.output.Finish(nil, nil)
		}
		return entry
	}
	entry.native.running = false
	if !e.Historical {
		entry.native.commandEnded = u.now()
	}
	if entry.native.output == nil {
		entry.native.output = u.session.outputs.New()
	}
	entry.native.output.Finish(&e.Text, nil)
	entry.outputTail, entry.outputOmit = appServerOutputTail(&e.Text)
	entry.native.collapsed = e.Historical
	if e.Failed {
		entry.native.status = "failed"
	} else if !e.Historical {
		entry.native.settled = u.now()
	}

	return entry
}

func (u *appServerUI) runtimeCommandOutput(e session.Event) {
	if e.Output == nil || e.ID == "" {
		return
	}
	var output *activityui.Output
	for _, v := range []*liveActivityView{u.view, u.agents} {
		for i, old := range v.entries {
			if old.CallID != e.ID || old.Kind != "tool" || old.native == nil {
				continue
			}
			entry := old.activityPaneEntry
			native := *old.native
			entry.native = &native
			if native.output == nil {
				native.output = u.session.outputs.New()
			}
			if output == nil {
				if native.output.View().Done {
					break
				}
				output = native.output
				output.Snapshot(e.Text, e.Output.Truncated)
				if e.Output.Done {
					output.Finish(nil, nil)
				}
			}
			native.output = output
			native.running = !e.Output.Done
			if e.Output.Done {
				native.commandEnded = u.now()
				if e.Failed {
					native.status = "failed"
				} else {
					native.settled = u.now()
				}
			}
			entry.outputTail, entry.outputOmit = appServerOutputTail(&e.Text)
			for _, segment := range native.segments {
				if segment.output != nil {
					entry.outputTail, entry.outputOmit = nil, 0
					break
				}
			}
			v.replaceEntry(i, entry, parseLiveActivity(entry))
			break
		}
	}
}

func (u *appServerUI) runtimeFinishCommand(id string, failed bool) {
	for _, v := range []*liveActivityView{u.view, u.agents} {
		for i, old := range v.entries {
			if old.CallID != id || old.Kind != "tool" || old.native == nil || !old.native.running || !old.native.background {
				continue
			}
			entry := old.activityPaneEntry
			native := *old.native
			entry.native = &native
			native.running, native.commandEnded = false, u.now()
			if native.output != nil {
				native.output.Finish(nil, nil)
			}
			if failed {
				native.status = "failed"
			} else {
				native.settled = u.now()
			}
			v.replaceEntry(i, entry, parseLiveActivity(entry))
		}
	}
}

func runtimeToolText(name, input, workspace string) (text, command string) {
	var args struct {
		Command     string `json:"command"`
		Path        string `json:"file_path"`
		Pattern     string `json:"pattern"`
		Directory   string `json:"path"`
		Description string `json:"description"`
		URL         string `json:"url"`
		Query       string `json:"query"`
	}
	if json.Unmarshal([]byte(input), &args) != nil {
		return name + "\n" + toolActivityFenced("", input), ""
	}
	path := func(value string) string { return commentaryCode(pathdisplay.ForWorkspace(workspace, value)) }
	switch name {
	case "Bash":
		return toolActivityShell(args.Command), args.Command
	case "Read":
		return "Read " + path(args.Path), ""
	case "Edit", "Write":
		return "Edit " + path(args.Path), ""
	case "Glob", "Grep":
		text = "Search " + commentaryCode(args.Pattern)
		if args.Directory != "" {
			text += " in " + path(args.Directory)
		}
		return text, ""
	case "WebFetch":
		return "Fetch " + commentaryCode(args.URL), ""
	case "WebSearch":
		return "Search " + commentaryCode(args.Query), ""
	case "Agent", "Task":
		return "Agent " + commentaryCode(args.Description), ""
	}
	if server, tool, ok := toolActivityMCPName(name); ok {
		return "Call " + commentaryCode(server+" / "+tool), ""
	}
	return strings.TrimSpace(name) + "\n" + toolActivityFenced("json", input), ""
}
