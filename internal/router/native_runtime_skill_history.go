package router

import "github.com/yusing/mekugi/internal/session"

type runtimeSkillScan struct {
	agentID string
	names   activeSkillSet
	tools   map[string]session.Event
	failed  bool
}

func (u *appServerUI) runtimeSkillHistory(e session.Event) {
	h := e.SkillHistory
	if h == nil {
		return
	}
	owner := "Main"
	if h.AgentID != "" {
		owner = runtimeTaskLane(h.AgentID)
	}
	switch h.Phase {
	case "start":
		u.runtime.skillScan = &runtimeSkillScan{agentID: h.AgentID, tools: make(map[string]session.Event)}
		for _, v := range []*liveActivityView{u.view, u.agents} {
			if v.skillHistory == nil {
				v.skillHistory = make(map[string]bool)
			}
			v.skillHistory[owner] = false
			delete(v.retiredSkills, owner)
			v.skills = nil
		}
	case "message", "done":
		scan := u.runtime.skillScan
		if scan == nil || scan.agentID != h.AgentID {
			return
		}
		for _, event := range h.Events {
			switch event.Kind {
			case "context":
				scan.names, scan.tools, scan.failed = nil, make(map[string]session.Event), false
			case "tool":
				if event.Role == "Skill" || event.Role == "Read" {
					scan.tools[event.ID] = event
				}
			case "tool_result":
				tool, found := scan.tools[event.ID]
				delete(scan.tools, event.ID)
				if !found || event.Failed {
					continue
				}
				if tool.Role == "Skill" && event.Skill == "" {
					scan.failed = true
					continue
				}
				text, _ := runtimeToolText(tool.Role, tool.Text, u.session.cwd)
				if tool.Role == "Skill" {
					text = "Skill " + commentaryCode(event.Skill)
				}
				entry := activityPaneEntry{Kind: "tool", Text: text, native: &liveActivityNativeItem{tool: tool.Role}}
				scan.names = scan.names.with(skillLoads(liveActivityRecord{activityPaneEntry: entry, blocks: parseLiveActivity(entry)})...)
			}
		}
		if h.Phase == "done" {
			for _, v := range []*liveActivityView{u.view, u.agents} {
				v.skillHistory[owner] = !e.Failed && !scan.failed
				if v.skillHistory[owner] {
					if v.retiredSkills == nil {
						v.retiredSkills = make(map[string]activeSkillSet)
					}
					v.retiredSkills[owner] = scan.names
				}
				v.skills = nil
			}
			if scan.failed && !e.Failed {
				u.setNotice("Could not restore active skills for "+owner+": native Skill receipt is incomplete; the count is unavailable", false)
			}
			u.runtime.skillScan = nil
		}
	}
	u.dirty = true
}
