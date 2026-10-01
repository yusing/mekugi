package router

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/yusing/mekugi/capturer"
)

// This panel owns only navigation. Accounting and retained detail belong to the
// invocation's capturer, including requests from other threads in the launch.
type sessionMetricsPanel struct {
	snapshot  capturer.MetricsSnapshot
	tab       int
	sequence  uint64 // Zero follows the newest retained exchange.
	refreshed time.Time
}

var sessionMetricViews = []string{"Overview", "Transport", "Exchanges"}

func (u *appServerUI) showSessionMetrics() {
	u.statusPanel = &appServerStatusReport{metrics: new(sessionMetricsPanel)}
	u.deleteDraftRange(0, len(u.draft))
	u.refreshSessionMetrics(true)
}

func (u *appServerUI) refreshSessionMetrics(force bool) {
	p := u.statusPanel
	if p == nil || p.metrics == nil {
		return
	}
	m := p.metrics
	if !force && u.now().Sub(m.refreshed) < time.Second {
		return
	}
	m.refreshed = u.now()
	if u.sessionCapture != nil {
		m.snapshot = u.sessionCapture.Snapshot()
	}
	u.renderSessionMetrics()
}

func (u *appServerUI) sessionMetricsKey(key string) bool {
	p := u.statusPanel
	m := p.metrics
	switch key {
	case "\x1b[C", "\t":
		m.tab = (m.tab + 1) % len(sessionMetricViews)
	case "\x1b[D", "\x1b[Z":
		m.tab = (m.tab + len(sessionMetricViews) - 1) % len(sessionMetricViews)
	case "[", "]":
		if m.tab != 2 || len(m.snapshot.Exchanges) == 0 {
			return true
		}
		index := m.exchangeIndex()
		if key == "[" {
			index = max(0, index-1)
		} else {
			index = min(len(m.snapshot.Exchanges)-1, index+1)
		}
		m.sequence = m.snapshot.Exchanges[index].Sequence
		if index == len(m.snapshot.Exchanges)-1 {
			m.sequence = 0
		}
	case "r":
		u.refreshSessionMetrics(true)
		return true
	default:
		return false
	}
	p.top = 0
	if u.shell != nil {
		u.shell.selection = nil
	}
	u.renderSessionMetrics()
	return true
}

func (m *sessionMetricsPanel) exchangeIndex() int {
	if m.sequence != 0 {
		for i, exchange := range m.snapshot.Exchanges {
			if exchange.Sequence == m.sequence {
				return i
			}
		}
		// Retention reclaimed the selected exchange. Follow newest again.
		m.sequence = 0
	}
	return len(m.snapshot.Exchanges) - 1
}

func (u *appServerUI) renderSessionMetrics() {
	p, m := u.statusPanel, u.statusPanel.metrics
	p.title = "Session · " + sessionMetricViews[m.tab]
	p.controls = "←→ views · ↑↓ scroll · r refresh · Esc close"
	if m.tab == 2 {
		p.controls = "←→ views · [ older / ] newer · ↑↓ scroll · Esc close"
	}
	p.fields, p.groups = nil, nil
	add := func(group, label string, value any) {
		text := fmt.Sprint(value)
		if text == "" {
			return
		}
		if !slices.Contains(p.groups, group) {
			p.groups = append(p.groups, group)
		}
		p.fields = append(p.fields, statusField{group: group, label: label, value: text})
	}
	payload := func(group, label string, bytes, tokens uint64) {
		add(group, label, fmt.Sprintf("%d bytes · %d estimated tokens", bytes, tokens))
	}
	usage := func(group string, input, cached, uncached, output, reasoning uint64) {
		for _, row := range []struct {
			label string
			value uint64
		}{{"Input", input}, {"Cached input", cached}, {"Uncached input", uncached}, {"Output", output}, {"Reasoning", reasoning}} {
			add(group, row.label, row.value)
		}
	}
	optional := func(group, label string, value *uint64) {
		if value == nil {
			add(group, label, "unavailable")
		} else {
			add(group, label, *value)
		}
	}
	s := m.snapshot
	if s.Schema == "" {
		add("Capture", "", "Metrics are unavailable for this frontend. Live launch metrics are not restored from saved sessions.")
		u.dirty = true
		return
	}
	switch m.tab {
	case 0:
		add("Requests", "Scope", "All threads in this launch · "+s.Mode)
		add("Requests", "Logical", s.Requests.Logical)
		add("Requests", "Attempts", s.Requests.ProviderAttempts)
		add("Requests", "Retries", s.Requests.Retries)
		add("Requests", "Completed", s.Requests.Completed)
		add("Requests", "Failed", s.Requests.Failed)
		add("Usage", "", "Provider-reported consumption, not local estimates. Missing normalized telemetry may default to zero; inspect Exchanges for explicit evidence.")
		usage("Usage", s.Usage.InputTokens, s.Usage.CachedInputTokens, s.Usage.UncachedInputTokens, s.Usage.OutputTokens, s.Usage.ReasoningTokens)
		add("Usage evidence", "", "Attempt coverage: complete categories, partial telemetry, legacy completeness unknown, or no usage observed.")
		add("Usage evidence", "Complete", s.Usage.CompleteAttempts)
		add("Usage evidence", "Partial", s.Usage.IncompleteAttempts)
		add("Usage evidence", "Legacy", s.Usage.UnknownAttempts)
		add("Usage evidence", "Missing", s.Usage.MissingAttempts)
		if s.Cache.ProviderCacheRate == nil {
			add("Cache", "Provider rate", "unavailable")
		} else {
			add("Cache", "Provider rate", fmt.Sprintf("%.1f%%", *s.Cache.ProviderCacheRate*100))
		}
		add("Cache", "", "Cached / input from observed usage. Partial or missing telemetry is not evidence of zero consumption or cache misses.")
		for _, row := range []struct {
			label string
			value uint64
		}{{"Records", s.Capture.Records}, {"Errors", s.Capture.CaptureErrors}, {"Incomplete", s.Capture.Incomplete}, {"Missing provider", s.Capture.MissingProvider}, {"Attempt gaps", s.Capture.AttemptGaps}, {"Write errors", s.Capture.WriteErrors}, {"Skipped", s.Capture.SkippedRequests}, {"Dropped detail", s.Capture.DroppedExchangeDetails}} {
			add("Capture", row.label, row.value)
		}
	case 1:
		add("Transport", "", "Exact wire bytes; decoded-content token estimates. Includes router-generated commentary.")
		for _, row := range []struct {
			label         string
			bytes, tokens uint64
		}{
			{"Client requests", s.Transport.ClientRequests.Bytes, s.Transport.ClientRequests.Tokens},
			{"Provider requests", s.Transport.ProviderAttemptRequests.Bytes, s.Transport.ProviderAttemptRequests.Tokens},
			{"Provider responses", s.Transport.ProviderResponses.Bytes, s.Transport.ProviderResponses.Tokens},
			{"Client responses", s.Transport.ClientResponses.Bytes, s.Transport.ClientResponses.Tokens},
			{"Client control requests", s.Transport.ClientControlRequests.Bytes, s.Transport.ClientControlRequests.Tokens},
			{"Client control responses", s.Transport.ClientControlResponses.Bytes, s.Transport.ClientControlResponses.Tokens},
			{"Provider control requests", s.Transport.ProviderControlRequests.Bytes, s.Transport.ProviderControlRequests.Tokens},
			{"Provider control responses", s.Transport.ProviderControlResponses.Bytes, s.Transport.ProviderControlResponses.Tokens},
		} {
			payload("Transport", row.label, row.bytes, row.tokens)
		}
		add("Semantic output", "", "Complete model-origin output arrays. Excludes router commentary and echoed response metadata.")
		payload("Semantic output", "Provider", s.Semantic.ProviderAttemptOutputs.Bytes, s.Semantic.ProviderAttemptOutputs.Tokens)
		payload("Semantic output", "Client", s.Semantic.ClientOutputs.Bytes, s.Semantic.ClientOutputs.Tokens)
		for _, boundary := range []string{"Provider tools", "Delivered tools"} {
			tools := s.ProviderTools
			if boundary == "Delivered tools" {
				tools = s.DeliveredTools
			}
			if len(tools) == 0 {
				add(boundary, "", "No observations")
			}
			for _, name := range slices.Sorted(maps.Keys(tools)) {
				tool := tools[name]
				add(boundary, name, fmt.Sprintf("%d calls", tool.Calls))
				payload(boundary, "Input", tool.InputBytes, tool.InputTokens)
				payload(boundary, "Items", tool.ItemBytes, tool.ItemTokens)
			}
		}
	case 2:
		if len(s.Exchanges) == 0 {
			add("Exchanges", "", "No completed exchanges yet. Active requests appear when they settle.")
			break
		}
		index := m.exchangeIndex()
		e := s.Exchanges[index]
		add("Exchange", "Retained", fmt.Sprintf("%d / %d · [ older / ] newer", index+1, len(s.Exchanges)))
		add("Exchange", "Sequence", e.Sequence)
		add("Exchange", "Thread", e.ThreadID)
		add("Exchange", "Model", e.Model)
		if e.Status != "" {
			add("Exchange", "Status", e.Status)
		}
		if e.RequestKind != "" {
			add("Exchange", "Kind", e.RequestKind)
		}
		add("Exchange", "Duration", fmt.Sprintf("%d ms", e.DurationMillis))
		if e.StatusCode != 0 {
			add("Exchange", "HTTP status", e.StatusCode)
		}
		add("Exchange", "Complete", e.ResponseComplete)
		add("Exchange", "Capture error", e.CaptureError)
		payload("Exchange", "Client request", e.ClientRequest.Bytes, e.ClientRequest.Tokens)
		payload("Exchange", "Client stream", e.ClientResponse.Bytes, e.ClientResponse.Tokens)
		payload("Exchange", "Model output", e.ClientFinalOutput.Bytes, e.ClientFinalOutput.Tokens)
		payload("Exchange", "Final text", e.ClientFinalText.Bytes, e.ClientFinalText.Tokens)
		if e.Usage == nil {
			add("Exchange", "Usage", "unavailable")
		} else {
			usage("Exchange", e.Usage.InputTokens, e.Usage.CachedInputTokens, e.Usage.UncachedInputTokens, e.Usage.OutputTokens, e.Usage.ReasoningTokens)
		}
		if d := e.CacheDiagnosis; d != nil {
			add("Cache diagnostics", "Previous", d.PreviousSequence)
			add("Cache diagnostics", "Client prefix", fmt.Sprintf("%s · %d common items · changed fields: %v", d.Client.Status, d.Client.CommonItems, d.Client.ChangedFields))
			add("Cache diagnostics", "Post replay", fmt.Sprintf("%s · %d common items · changed fields: %v", d.Projected.Status, d.Projected.CommonItems, d.Projected.ChangedFields))
			add("Cache diagnostics", "Provider prefix", fmt.Sprintf("%s · %d common items · changed fields: %v", d.Provider.Status, d.Provider.CommonItems, d.Provider.ChangedFields))
			add("Cache diagnostics", "Route key", d.Routing)
			add("Cache diagnostics", "Request key", d.RequestKey)
			add("Cache diagnostics", "Turn state", cmp.Or(d.TurnStateForwarding, "unavailable"))
		} else {
			add("Cache diagnostics", "", "Prefix comparisons unavailable on first or partial observations.")
		}
		if e.RequestKind == "compaction" {
			add("Compaction", "Answerer", e.CompactionAnswer)
			optional("Compaction", "Summary bytes", e.CompactionSummaryBytes)
			optional("Compaction", "Changes", e.CompactionChanges)
			optional("Compaction", "Failures", e.CompactionFailures)
			add("Compaction", "", "Router answers invoke no provider. Missing counts are unavailable, not zero; no savings are estimated.")
		}
		if j := e.Journal; j != nil {
			add("Journal", "", "Cumulative thread snapshot, never sum across exchanges.")
			add("Journal", "Started", j.StartedAt)
			add("Journal", "Sequence", j.Sequence)
			add("Journal", "Standalone", j.StandaloneRequests)
			add("Journal", "Final answers", j.FinalAnswers)
			add("Journal", "Answer bytes", j.FinalAnswerBytes)
			add("Journal", "Empty outcomes", j.EmptyOutcomes)
			if j.LastOutcomeEmpty != nil {
				add("Journal", "Last empty", *j.LastOutcomeEmpty)
			}
			for _, op := range slices.Sorted(maps.Keys(j.Operations)) {
				add("Journal", op, j.Operations[op])
			}
		}
		if len(e.ProviderAttempts) == 0 {
			add("Provider attempts", "", "No provider attempt observed")
		}
		for _, a := range e.ProviderAttempts {
			group := fmt.Sprintf("Provider attempt %d", a.Attempt)
			add(group, "Model", a.Model)
			transport := cmp.Or(a.Transport, "http")
			if transport == "websocket" {
				transport = "WebSocket"
			} else if transport == "http" {
				transport = "HTTP"
			}
			add(group, "Transport", transport)
			if a.Status != "" {
				add(group, "Status", a.Status)
			}
			add(group, "Duration", fmt.Sprintf("%d ms", a.DurationMillis))
			if a.StatusCode != 0 {
				add(group, "HTTP status", a.StatusCode)
			}
			add(group, "Complete", a.ResponseComplete)
			add(group, "Capture error", a.CaptureError)
			payload(group, "Request", a.Request.Bytes, a.Request.Tokens)
			if a.ProjectedRequest != nil {
				payload(group, "Post replay", a.ProjectedRequest.Bytes, a.ProjectedRequest.Tokens)
			}
			payload(group, "Stream", a.Response.Bytes, a.Response.Tokens)
			payload(group, "Model output", a.FinalOutput.Bytes, a.FinalOutput.Tokens)
			payload(group, "Final text", a.FinalText.Bytes, a.FinalText.Tokens)
			if a.Usage == nil {
				add(group, "Usage", "unavailable")
			} else {
				usage(group, a.Usage.InputTokens, a.Usage.CachedInputTokens, a.Usage.UncachedInputTokens, a.Usage.OutputTokens, a.Usage.ReasoningTokens)
				switch {
				case a.Usage.CompleteAttempts != 0:
					add(group, "Usage evidence", "complete")
				case a.Usage.IncompleteAttempts != 0:
					add(group, "Usage evidence", "partial")
				default:
					add(group, "Usage evidence", "legacy completeness unknown")
				}
			}
			if evidence := a.ProviderResponse; evidence != nil {
				add(group, "Response model", cmp.Or(evidence.Model, "unavailable"))
				add(group, "Header model", cmp.Or(evidence.HeaderModel, "unavailable"))
				add(group, "Request ID", cmp.Or(evidence.RequestID, "unavailable"))
				add(group, "Cached telemetry", evidence.CachedTokensState)
				if evidence.CachedTokensState == "present" {
					optional(group, "Explicit cached", evidence.CachedTokens)
				}
			} else {
				add(group, "Telemetry", "unavailable")
			}
			for _, tool := range a.Tools {
				add(group, "Tool", tool.Name+" · "+tool.CallID)
				payload(group, "Tool input", tool.InputBytes, tool.InputTokens)
				payload(group, "Tool item", tool.ItemBytes, tool.ItemTokens)
			}
		}
		if len(e.DeliveredTools) == 0 {
			add("Delivered tools", "", "No observations")
		}
		for _, tool := range e.DeliveredTools {
			add("Delivered tools", "Tool", tool.Name+" · "+tool.CallID)
			payload("Delivered tools", "Input", tool.InputBytes, tool.InputTokens)
			payload("Delivered tools", "Item", tool.ItemBytes, tool.ItemTokens)
		}
	}
	u.dirty = true
}
