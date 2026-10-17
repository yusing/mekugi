package terminalsession

import (
	"cmp"
	"fmt"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/libraries/pathdisplay"
	usagepresentation "github.com/yusing/mekugi/modules/usage/presentation"
	"slices"
)

var metricViews = []string{"Overview", "Transport", "Exchanges"}

// Source: apps/mekugi/internal/router/app_server_session_metrics.go:97:299@630ce384.
func (p *StatusPanel) renderMetrics() {
	p.Title = "Session · " + metricViews[p.Tab]
	p.Controls = "←→ views · ↑↓ scroll · r refresh · Esc close"
	if p.Tab == 2 {
		p.Controls = "←→ views · [ older / ] newer · ↑↓ scroll · Esc close"
	}
	p.Fields, p.Groups = nil, nil
	add := func(group, label string, value any) {
		text := fmt.Sprint(value)
		if text == "" {
			return
		}
		if !slices.Contains(p.Groups, group) {
			p.Groups = append(p.Groups, group)
		}
		p.Fields = append(p.Fields, sessionpresentation.StatusField{Group: group, Label: label, Value: text})
	}
	payload := func(group, label string, bytes uint64) {
		add(group, label, fmt.Sprintf("%d bytes", bytes))
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
	s := *p.body.Metrics
	if p.Tab == 0 && p.body.DebugDirectory != "" {
		add("Debug", "Directory", pathdisplay.ForWorkspace(p.body.Workspace, p.body.DebugDirectory))
		p.Fields[len(p.Fields)-1].Path = true
	}
	if p.Tab == 0 && s.OutputDeduplication.Projections > 0 {
		add("Output deduplication", "Occurrences", s.OutputDeduplication.Occurrences)
		add("Output deduplication", "Saved tokens (estimated)", s.OutputDeduplication.SavedTokens)
		add("Output deduplication", "Unmeasured projections", s.OutputDeduplication.Unmeasured)
		add("Output deduplication", "", "Launch totals per request projection, including repeated history. Net local estimate includes reference overhead; not provider billing savings.")
	}
	if p.Tab == 0 && s.StorageWrites != nil {
		add("Storage writes", "Application bytes", s.StorageWrites.Bytes)
		add("Storage writes", "", s.StorageWrites.Scope)
	}
	if s.Schema == "" {
		add("Capture", "", "Metrics are unavailable for this frontend. Live launch metrics are not restored from saved sessions.")
		return
	}
	switch p.Tab {
	case 0:
		add("Requests", "Scope", "All threads in this launch · "+s.Mode)
		add("Requests", "Logical", s.Requests.Logical)
		add("Requests", "Attempts", s.Requests.ProviderAttempts)
		add("Requests", "Retries", s.Requests.Retries)
		add("Requests", "Completed", s.Requests.Completed)
		add("Requests", "Failed", s.Requests.Failed)
		add("Usage", "", "Provider-reported consumption, not local estimates. Missing normalized telemetry may default to zero; inspect Exchanges for explicit evidence.")
		usage("Usage", s.Usage.InputTokens, s.Usage.CachedInputTokens, s.Usage.UncachedInputTokens, s.Usage.OutputTokens, s.Usage.ReasoningTokens)
		if throughput := usagepresentation.OutputThroughputLabel(s.Usage.OutputThroughput); throughput != "" {
			add("Usage", "Average output", throughput)
			add("Usage", "Measured requests", s.Usage.OutputThroughput.MeasuredRequests)
			add("Usage", "Measured duration", fmt.Sprintf("%.3f s", float64(s.Usage.OutputThroughput.DurationNanos)/1e9))
		} else {
			add("Usage", "Average output", "unavailable")
		}
		add("Usage", "Throughput basis", "Measured output tokens / summed provider-request seconds, including latency and retries. Unmeasured requests excluded; not session wall time or mean request TPS.")
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
		add("Transport", "", "Exact wire bytes, including router-generated commentary.")
		for _, row := range []struct {
			label string
			bytes uint64
		}{
			{"Client requests", s.Transport.ClientRequests.Bytes},
			{"Provider requests", s.Transport.ProviderAttemptRequests.Bytes},
			{"Provider responses", s.Transport.ProviderResponses.Bytes},
			{"Client responses", s.Transport.ClientResponses.Bytes},
			{"Client control requests", s.Transport.ClientControlRequests.Bytes},
			{"Client control responses", s.Transport.ClientControlResponses.Bytes},
			{"Provider control requests", s.Transport.ProviderControlRequests.Bytes},
			{"Provider control responses", s.Transport.ProviderControlResponses.Bytes},
		} {
			payload("Transport", row.label, row.bytes)
		}
	case 2:
		if len(s.Exchanges) == 0 {
			add("Exchanges", "", "No completed exchanges yet. Active requests appear when they settle.")
			break
		}
		index := p.exchangeIndex()
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
		payload("Exchange", "Client request", e.ClientRequest.Bytes)
		payload("Exchange", "Client stream", e.ClientResponse.Bytes)
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
			payload(group, "Request", a.Request.Bytes)
			payload(group, "Stream", a.Response.Bytes)
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
				add(group, "Service tier", cmp.Or(evidence.ServiceTier, "unavailable"))
				add(group, "Response model", cmp.Or(evidence.Model, "unavailable"))
				add(group, "Header model", cmp.Or(evidence.HeaderModel, "unavailable"))
				add(group, "Request ID", cmp.Or(evidence.RequestID, "unavailable"))
				add(group, "Cached telemetry", evidence.CachedTokensState)
				if evidence.CachedTokensState == "present" {
					optional(group, "Explicit cached", evidence.CachedTokens)
				}
			} else {
				add(group, "Service tier", "unavailable")
				add(group, "Telemetry", "unavailable")
			}
		}
	}
}
