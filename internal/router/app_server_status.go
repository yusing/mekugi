package router

import (
	"cmp"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/internal/appserver"
)

// Source: codex-rs/tui/src/chatwidget/status_controls.rs, add_status_output;
// codex-rs/tui/src/status/card.rs, StatusHistoryCell::content_lines.
// Unlike Codex, status is an ephemeral panel, never a transcript entry.
// Each asynchronous refresh belongs only to the panel that requested it.
type appServerStatusReport struct {
	rect      terminalRect // Selectable body, relative to Main.
	fields    []statusField
	top, rows int
	body      []statusField
	tokens    []statusField
	account   []statusField
}

// Values remain structured through rendering; meters are host observations,
// not percentages reconstructed from presentation text.
type statusField struct {
	group, label, value, detail string
	remaining                   *float64
	alert                       bool
}

func statusNotice(value string) []statusField {
	return []statusField{{group: "Account", value: value}}
}

type appServerStatusConfig struct {
	Provider string         `json:"modelProvider"`
	Approval jsontext.Value `json:"approvalPolicy"`
	Sandbox  struct {
		Type string `json:"type"`
	} `json:"sandbox"`
	Instructions []string `json:"instructionSources"`
}

func (u *appServerUI) showStatus() error {
	var rows []statusField
	group := "Model"
	add := func(label, value string) {
		if value != "" {
			rows = append(rows, statusField{group: group, label: label, value: value})
		}
	}
	add("Model", u.model)
	add("Reasoning", u.reasoningEffort)
	add("Service tier", u.serviceTier)
	add("Model provider", u.statusConfig.Provider)
	group = "Session"
	add("Directory", u.session.cwd)
	add("Session", u.thread)
	var approval string
	if len(u.statusConfig.Approval) != 0 {
		if json.Unmarshal(u.statusConfig.Approval, &approval) != nil {
			approval = string(u.statusConfig.Approval)
		}
		if approval == "null" {
			approval = ""
		}
	}
	group = "Permissions"
	add("Approvals", approval)
	add("Sandbox", u.statusConfig.Sandbox.Type)
	if len(u.statusConfig.Instructions) > 0 {
		group = "Session"
		add("Agents.md", strings.Join(u.statusConfig.Instructions, ", "))
	}
	report := new(appServerStatusReport)
	for _, agent := range u.session.agents {
		if agent.Name != "/root" {
			continue
		}
		if agent.ContextKnown && agent.ContextWindow > 0 {
			remaining := 100 * (1 - min(1, float64(agent.ContextTokens)/float64(agent.ContextWindow)))
			rows = append(rows, statusField{group: "Usage", label: "Context window", value: fmt.Sprintf("%.0f%% left", remaining), detail: fmt.Sprintf("%s used / %s", formatUsageTokens(agent.ContextTokens), formatUsageTokens(agent.ContextWindow)), remaining: new(remaining)})
		}
		if agent.InputTokens > 0 || agent.OutputTokens > 0 {
			report.tokens = []statusField{{group: "Usage", label: "Token usage", value: fmt.Sprintf("%s input · %s output", formatUsageTokens(agent.InputTokens), formatUsageTokens(agent.OutputTokens))}}
		}
	}
	report.body = rows
	u.statusPanel = report
	u.renderStatus(report, statusNotice("Loading account…"))
	u.deleteDraftRange(0, len(u.draft))
	return u.requestStatus("account/read", map[string]any{"refreshToken": false}, report)
}

func (u *appServerUI) requestStatus(method string, params any, report *appServerStatusReport) error {
	id, err := u.client.Send(method, params, true)
	if err != nil {
		u.renderStatus(report, []statusField{{group: "Account", value: "Status refresh failed: " + err.Error(), alert: true}})
		return err
	}
	if u.statusReports == nil {
		u.statusReports = make(map[string]*appServerStatusReport)
	}
	u.statusReports[id] = report
	u.requests[id] = method
	return nil
}

func (u *appServerUI) renderStatus(report *appServerStatusReport, extra []statusField) {
	report.fields = slices.Concat(report.body, report.account, extra)
	u.dirty = true
}

// Source: codex-rs/app-server-protocol/src/protocol/v2/account.rs.
func (u *appServerUI) statusMessage(method string, m appserver.Message) (bool, error) {
	report := u.statusReports[string(m.ID)]
	if report == nil {
		return false, nil
	}
	delete(u.statusReports, string(m.ID))
	if report != u.statusPanel {
		return true, nil
	}
	fail := func(message string) (bool, error) {
		u.renderStatus(report, []statusField{{group: "Account", value: message, alert: true}})
		return true, nil
	}
	if m.Error != nil {
		return fail(method + ": " + m.Error.Message)
	}
	if method == "account/read" {
		var result struct {
			Account *struct {
				Type  string `json:"type"`
				Email string `json:"email"`
				Plan  string `json:"planType"`
			} `json:"account"`
		}
		if err := json.Unmarshal(m.Result, &result); err != nil {
			return fail("Account response could not be read")
		}
		if result.Account == nil {
			u.renderStatus(report, report.tokens)
			return true, nil
		}
		account := result.Account
		switch account.Type {
		case "chatgpt":
			report.account = []statusField{{group: "Account", label: "Sign-in", value: "ChatGPT"}}
			if account.Email != "" {
				report.account = append(report.account, statusField{group: "Account", label: "Email", value: account.Email})
			}
			if account.Plan != "" && account.Plan != "unknown" {
				report.account = append(report.account, statusField{group: "Account", label: "Plan", value: account.Plan})
			}
			u.renderStatus(report, statusNotice("Loading usage limits…"))
			return true, u.requestStatus("account/rateLimits/read", nil, report)
		case "apiKey":
			report.account = []statusField{{group: "Account", label: "Sign-in", value: "API key"}}
		case "amazonBedrock":
			report.account = []statusField{{group: "Account", label: "Sign-in", value: "Amazon Bedrock"}}
		}
		u.renderStatus(report, report.tokens)
		return true, nil
	}
	var result struct {
		Limits appServerStatusLimit            `json:"rateLimits"`
		ByID   map[string]appServerStatusLimit `json:"rateLimitsByLimitId"`
	}
	if err := json.Unmarshal(m.Result, &result); err != nil {
		return fail("Usage limits response could not be read")
	}
	var rows []statusField
	if len(result.ByID) == 0 {
		rows = result.Limits.rows("Usage")
	} else {
		for _, id := range slices.Sorted(maps.Keys(result.ByID)) {
			rows = append(rows, result.ByID[id].rows(id)...)
		}
	}
	rows = append(rows, statusField{group: "Account", label: "Usage details", value: "https://chatgpt.com/codex/settings/usage"})
	u.renderStatus(report, rows)
	return true, nil
}

type appServerStatusLimit struct {
	Name      string                 `json:"limitName"`
	Primary   *appServerStatusWindow `json:"primary"`
	Secondary *appServerStatusWindow `json:"secondary"`
	Credits   *struct {
		Unlimited bool    `json:"unlimited"`
		Balance   *string `json:"balance"`
	} `json:"credits"`
}
type appServerStatusWindow struct {
	Used    float64 `json:"usedPercent"`
	Minutes *int64  `json:"windowDurationMins"`
	Resets  *int64  `json:"resetsAt"`
}

func (limit appServerStatusLimit) rows(id string) []statusField {
	var rows []statusField
	name := cmp.Or(limit.Name, id)
	for i, window := range []*appServerStatusWindow{limit.Primary, limit.Secondary} {
		if window == nil {
			continue
		}
		label := []string{"primary", "secondary"}[i]
		if window.Minutes != nil {
			minutes := *window.Minutes
			switch {
			case minutes == 10080:
				label = "weekly"
			case minutes > 0 && minutes%60 == 0:
				label = fmt.Sprintf("%dh", minutes/60)
			default:
				label = fmt.Sprintf("%dm", minutes)
			}
		}
		remaining := max(0, min(100, 100-window.Used))
		row := statusField{group: "Usage", label: name + " " + label + " limit", value: fmt.Sprintf("%.0f%% left", remaining), remaining: new(remaining)}
		if window.Resets != nil {
			row.detail = "resets " + time.Unix(*window.Resets, 0).Local().Format("Jan 2 15:04 MST")
		}
		rows = append(rows, row)
	}
	if limit.Credits != nil {
		if limit.Credits.Unlimited {
			rows = append(rows, statusField{group: "Usage", label: name + " credits", value: "unlimited"})
		} else if limit.Credits.Balance != nil {
			rows = append(rows, statusField{group: "Usage", label: name + " credits", value: *limit.Credits.Balance})
		}
	}
	return rows
}

func (u *appServerUI) statusPanelKey(key string) bool {
	p := u.statusPanel
	if p == nil {
		return false
	}
	switch key {
	case "\x1b", "q", "\r", "\x03":
		if u.shell != nil {
			u.shell.selection = nil
		}
		u.statusPanel = nil
	case "\x1b[A":
		p.top = max(0, p.top-1)
	case "\x1b[B":
		p.top++
	case "\x1b[5~":
		p.top = max(0, p.top-max(1, p.rows))
	case "\x1b[6~":
		p.top += max(1, p.rows)
	case "\x1b[H", "\x1b[1~":
		p.top = 0
	}
	u.dirty = true
	return true
}
