package launch

import (
	"cmp"
	"encoding/json/jsontext"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/modules/conversation/composer"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"

	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	json "encoding/json/v2"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/backends/codex/appserver"
)

// Source: codex-rs/tui/src/chatwidget/status_controls.rs, add_status_output;
// codex-rs/tui/src/status/card.rs, StatusHistoryCell::content_lines.
// Unlike Codex, status is an ephemeral panel, never a transcript entry.
// Each asynchronous refresh belongs only to the panel that requested it.
type appServerStatusReport struct {
	*sessionpresentation.PanelState
	metrics *sessionMetricsPanel
	view    string
}

func newStatusReport(presentation *sessionpresentation.Model, debugDirectory string) *appServerStatusReport {
	return &appServerStatusReport{PanelState: presentation.OpenPanel(debugDirectory)}
}

func statusNotice(value string) []sessionpresentation.StatusField {
	return []sessionpresentation.StatusField{{Group: "Account", Value: value}}
}

type appServerStatusConfig struct {
	Provider string         `json:"modelProvider"`
	Approval jsontext.Value `json:"approvalPolicy"`
	Sandbox  struct {
		Type string `json:"type"`
	} `json:"sandbox"`
	Instructions []string `json:"instructionSources"`
}

func showStatus(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring **appServerActivityRestore, side **sessioncontrol.Side, panel **appServerStatusReport, resumePicker **appServerResumePicker, owned *map[string]bool, statusConfig appServerStatusConfig, debugDirectory string) error {
	var rows []sessionpresentation.StatusField
	group := "Model"
	add := func(label, value string) {
		if value != "" {
			rows = append(rows, sessionpresentation.StatusField{Group: group, Label: label, Value: value})
		}
	}
	add("Model", settings.Model)
	add("Reasoning", settings.Effort)
	add("Service tier", tiers.Effective(settings.Model, settings.Tier, control.Thread))
	add("Model provider", statusConfig.Provider)
	group = "Session"
	add("Directory", attached.cwd)
	add("Session", control.Thread)
	var approval string
	if len(statusConfig.Approval) != 0 {
		if json.Unmarshal(statusConfig.Approval, &approval) != nil {
			approval = string(statusConfig.Approval)
		}
		if approval == "null" {
			approval = ""
		}
	}
	group = "Permissions"
	add("Approvals", approval)
	add("Sandbox", statusConfig.Sandbox.Type)
	if len(statusConfig.Instructions) > 0 {
		group = "Session"
		add("Agents.md", strings.Join(statusConfig.Instructions, ", "))
	}
	report := newStatusReport(attached.presentation, debugDirectory)
	report.view = attached.view
	for _, agent := range attached.session.Roster() {
		if agent.Name != "/root" {
			continue
		}
		if agent.ContextKnown && agent.ContextWindow > 0 {
			remaining := 100 * (1 - min(1, float64(agent.ContextTokens)/float64(agent.ContextWindow)))
			rows = append(rows, sessionpresentation.StatusField{Group: "Usage", Label: "Context window", Value: fmt.Sprintf("%.0f%% left", remaining), Detail: fmt.Sprintf("%s used / %s", activityui.FormatUsageTokens(agent.ContextTokens), activityui.FormatUsageTokens(agent.ContextWindow)), Remaining: new(remaining)})
		}
		if agent.InputTokens > 0 || agent.OutputTokens > 0 {
			report.Tokens = []sessionpresentation.StatusField{{Group: "Usage", Label: "Token usage", Value: fmt.Sprintf("%s input · %s output", activityui.FormatUsageTokens(agent.InputTokens), activityui.FormatUsageTokens(agent.OutputTokens))}}
		}
	}
	report.Body = rows
	(*panel) = report
	report.Render(statusNotice("Loading account…"))

	deleteDraftRange(attached, terminal, control, picker, editor, settings, tiers, title, backend, (*restoring) != nil, (*side), clock, questions, approvals, (*panel) != nil || (*resumePicker) != nil, (*owned), 0, len((*editor).Text))
	return requestStatus(control.Requests, "account/read", map[string]any{"refreshToken": false}, report)
}

func requestStatus(requests *sessioncontrol.Requests[appServerRequest], method string, params any, report *appServerStatusReport) error {
	_, err := requests.Send(method, params, viewRequestScope(report.view), appServerRequest{status: report, view: report.view})
	if err != nil {
		report.Render([]sessionpresentation.StatusField{{Group: "Account", Value: "Status refresh failed: " + err.Error(), Alert: true}})

		return err
	}
	return nil
}

// Source: codex-rs/app-server-protocol/src/protocol/v2/account.rs.
func statusMessage(requests *sessioncontrol.Requests[appServerRequest], panel *appServerStatusReport, method string, m appserver.Message, report *appServerStatusReport) (bool, error) {
	if report == nil {
		return false, nil
	}
	if report != panel {
		return true, nil
	}
	fail := func(message string) (bool, error) {
		report.Render([]sessionpresentation.StatusField{{Group: "Account", Value: message, Alert: true}})

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
			report.Render(report.Tokens)

			return true, nil
		}
		account := result.Account
		switch account.Type {
		case "chatgpt":
			report.Account = []sessionpresentation.StatusField{{Group: "Account", Label: "Sign-in", Value: "ChatGPT"}}
			if account.Email != "" {
				report.Account = append(report.Account, sessionpresentation.StatusField{Group: "Account", Label: "Email", Value: account.Email})
			}
			if account.Plan != "" && account.Plan != "unknown" {
				report.Account = append(report.Account, sessionpresentation.StatusField{Group: "Account", Label: "Plan", Value: account.Plan})
			}
			report.Render(statusNotice("Loading usage limits…"))

			return true, requestStatus(requests, "account/rateLimits/read", nil, report)
		case "apiKey":
			report.Account = []sessionpresentation.StatusField{{Group: "Account", Label: "Sign-in", Value: "API key"}}
		case "amazonBedrock":
			report.Account = []sessionpresentation.StatusField{{Group: "Account", Label: "Sign-in", Value: "Amazon Bedrock"}}
		}
		report.Render(report.Tokens)

		return true, nil
	}
	var result struct {
		Limits appServerStatusLimit            `json:"rateLimits"`
		ByID   map[string]appServerStatusLimit `json:"rateLimitsByLimitId"`
		Resets *appServerStatusResets          `json:"rateLimitResetCredits"`
	}
	if err := json.Unmarshal(m.Result, &result); err != nil {
		return fail("Usage limits response could not be read")
	}
	var rows []sessionpresentation.StatusField
	if len(result.ByID) == 0 {
		rows = result.Limits.rows("Usage")
	} else {
		for _, id := range slices.Sorted(maps.Keys(result.ByID)) {
			rows = append(rows, result.ByID[id].rows(id)...)
		}
	}
	rows = append(rows, result.Resets.rows()...)
	rows = append(rows, sessionpresentation.StatusField{Group: "Account", Label: "Usage details", Value: "https://chatgpt.com/codex/settings/usage"})
	report.Render(rows)

	return true, nil
}

// Source: codex-rs/app-server-protocol/src/protocol/v2/account.rs:348:381@5a9eb145
// RateLimitResetCreditsSummary. Details can be absent or capped independently of count.
type appServerStatusResets struct {
	Available *int64 `json:"availableCount"`
	Credits   []struct {
		Type    string `json:"resetType"`
		Status  string `json:"status"`
		Expires *int64 `json:"expiresAt"`
		Title   string `json:"title"`
	} `json:"credits"`
}

func (resets *appServerStatusResets) rows() []sessionpresentation.StatusField {
	if resets == nil || resets.Available == nil {
		return nil
	}
	const group = "Usage limit resets"
	rows := []sessionpresentation.StatusField{{Group: group, Label: "Available", Value: fmt.Sprint(*resets.Available)}}
	for _, credit := range resets.Credits {
		if credit.Status != "available" {
			continue
		}
		title := credit.Title
		if title == "" && credit.Type == "codexRateLimits" {
			title = "Full reset"
		}
		value := "Does not expire"
		if credit.Expires != nil {
			value = "Expires " + time.Unix(*credit.Expires, 0).Local().Format("Jan 2 15:04 UTC-07:00")
		}
		rows = append(rows, sessionpresentation.StatusField{Group: group, Label: cmp.Or(title, "Reset"), Value: value})
	}
	return rows
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

func (limit appServerStatusLimit) rows(id string) []sessionpresentation.StatusField {
	var rows []sessionpresentation.StatusField
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
		row := sessionpresentation.StatusField{Group: "Usage", Label: name + " " + label + " limit", Value: fmt.Sprintf("%.0f%% left", remaining), Remaining: new(remaining)}
		if window.Resets != nil {
			row.Detail = "resets " + time.Unix(*window.Resets, 0).Local().Format("Jan 2 15:04 UTC-07:00")
		}
		rows = append(rows, row)
	}
	if limit.Credits != nil {
		if limit.Credits.Unlimited {
			rows = append(rows, sessionpresentation.StatusField{Group: "Usage", Label: name + " credits", Value: "unlimited"})
		} else if limit.Credits.Balance != nil {
			rows = append(rows, sessionpresentation.StatusField{Group: "Usage", Label: name + " credits", Value: *limit.Credits.Balance})
		}
	}
	return rows
}

func applyPanelAction(attached *attachedPresentation, terminal *terminalsession.Attachment, clock func() time.Time, panel **appServerStatusReport, capture *transportcapture.Recorder, action sessionpresentation.PanelAction) {
	if action.Kind == sessionpresentation.PanelNoAction {
		return
	}
	if err := attached.presentation.AdmitPanel(action); err != nil {
		setNotice(&attached.feedback, terminal, presentationTime(clock), err.Error(), true)
		return
	}
	switch action.Kind {
	case sessionpresentation.PanelClose:
		(*panel) = nil
		attached.presentation.ClosePanel()
	case sessionpresentation.PanelMetrics:
		if (*panel).Metrics == nil || action.Tab < 0 || action.Tab > 2 {
			return
		}
		exchanges := (*panel).Metrics.Exchanges
		if len(exchanges) > 0 && action.Exchange == exchanges[len(exchanges)-1].Sequence {
			action.Exchange = 0
		}
		(*panel).MetricTab, (*panel).MetricExchange = action.Tab, action.Exchange
		(*panel).RenderMetrics()
	case sessionpresentation.PanelRefresh:
		refreshSessionMetrics((*panel), capture, presentationTime(clock), true)
	}
}
