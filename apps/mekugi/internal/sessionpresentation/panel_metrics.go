package sessionpresentation

import (
	"cmp"
	"fmt"

	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/libraries/pathdisplay"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
	usagepresentation "github.com/yusing/mekugi/modules/usage/presentation"
)

// MetricViews names the /session views in tab order.
var MetricViews = []string{"Overview", "Transport", "Exchanges", "Features"}

// MetricFeatures identifies the page whose native counters are read on demand.
const MetricFeatures = 3

// FeatureCounts are model-facing feature uses and counted attempts in this launch.
type FeatureCounts struct {
	observation.FeatureUse
	OutputDeduplication transportcapture.OutputDeduplicationMetrics
}

// FeatureMetrics separates the selected thread from the sum of all of its
// admitted descendants, including settled ones.
type FeatureMetrics struct {
	Self, Descendants FeatureCounts
	// Thread names Self for display; Agents counts the summed descendants.
	Thread string
	Agents int
	// DeduplicationOff distinguishes a disabled feature from zero receipts.
	DeduplicationOff bool
	Unavailable      bool
}

// NewFeatureMetrics joins native counts with capture-owned deduplication receipts
// over the same admitted threads. Failed native reads are not measured zeros.
func NewFeatureMetrics(thread, name string, snapshot *transportcapture.MetricsSnapshot, deduplicate bool, use FeatureSnapshot) *FeatureMetrics {
	metrics := FeatureMetrics{Thread: name, DeduplicationOff: !deduplicate, Unavailable: use.Err != nil}
	metrics.Self.FeatureUse, metrics.Descendants.FeatureUse = use.Self, use.Descendants
	descendants := use.Threads
	metrics.Agents = len(descendants)
	if snapshot != nil {
		metrics.Self.OutputDeduplication = snapshot.OutputDeduplicationThreads[thread]
		for _, id := range descendants {
			metrics.Descendants.OutputDeduplication.Add(snapshot.OutputDeduplicationThreads[id])
		}
	}
	return &metrics
}

// featureFields compares Self with Descendants in one table per feature family.
// Each family's first row is its total; later rows count subsets or failed attempts.
func featureFields(f *FeatureMetrics, captured bool) (fields []StatusField) {
	if f == nil {
		return []StatusField{{Group: "Features", Value: "Feature use is unavailable for this frontend. Live launch counts are not restored from saved sessions."}}
	}
	fields = append(fields, StatusField{Group: "Features", Value: fmt.Sprintf("Self is %s; Descendants sums its admitted agents (%d), including completed ones. Counts cover this launch and are not restored on resume.", cmp.Or(f.Thread, "the displayed thread"), f.Agents)})
	type row struct {
		label             string
		self, descendants any
	}
	table := func(group string, rows ...row) {
		fields = append(fields, StatusField{Group: group, Heading: true, Values: []string{"Self", "Descendants"}})
		for _, r := range rows {
			values := []string{fmt.Sprint(r.self), fmt.Sprint(r.descendants)}
			if f.Unavailable && group != "Output deduplication" {
				values = []string{"unavailable", "unavailable"}
			}
			fields = append(fields, StatusField{Group: group, Label: r.label, Values: values})
		}
	}
	self, desc := f.Self, f.Descendants
	table("Compaction", row{"Total", self.Compactions, desc.Compactions}, row{"Self-initiated", self.SelfCompactions, desc.SelfCompactions}, row{"With preload", self.CompactionPreloads, desc.CompactionPreloads}, row{"Self completed", self.SelfCompactionsCompleted, desc.SelfCompactionsCompleted}, row{"Self failed", self.SelfCompactionFailures, desc.SelfCompactionFailures})
	table("Spawns", row{"Total", self.Spawns, desc.Spawns}, row{"With preload", self.SpawnPreloads, desc.SpawnPreloads}, row{"Preload rejected", self.PreloadRejections, desc.PreloadRejections})
	table("Delegate finals", row{"Completed", self.Finals, desc.Finals}, row{"With edit ranges", self.CompletionChanges, desc.CompletionChanges})
	table("Messages", row{"Sent", self.Messages, desc.Messages}, row{"With btw", self.SideQuestions, desc.SideQuestions}, row{"btw rejected", self.SideQuestionRejections, desc.SideQuestionRejections}, row{"btw failed", self.SideQuestionFailures, desc.SideQuestionFailures})
	table("Waits", row{"Registered", self.Waits, desc.Waits}, row{"All mode", self.AllWaits, desc.AllWaits}, row{"Progress enabled", self.ProgressWaits, desc.ProgressWaits}, row{"Progress delivered", self.ProgressYields, desc.ProgressYields})
	table("Source-bearing sends", row{"Total", self.AssignmentSources + self.MessageSources, desc.AssignmentSources + desc.MessageSources}, row{"Assignments", self.AssignmentSources, desc.AssignmentSources}, row{"Messages", self.MessageSources, desc.MessageSources})
	switch {
	case f.DeduplicationOff:
		fields = append(fields, StatusField{Group: "Output deduplication", Value: "Off for this launch."})
	case !captured:
		fields = append(fields, StatusField{Group: "Output deduplication", Value: "Unavailable for this frontend."})
	default:
		s, d := self.OutputDeduplication, desc.OutputDeduplication
		table("Output deduplication", row{"Occurrences", s.Occurrences, d.Occurrences}, row{"Saved tokens (est.)", s.SavedTokens, d.SavedTokens}, row{"Unmeasured", s.Unmeasured, d.Unmeasured})
		fields = append(fields, StatusField{Group: "Output deduplication", Value: "Occurrences count receipts per request projection, including repeated history. Saved tokens are a net local estimate after reference overhead, negative when references cost more than the output they replace, and not provider billing savings. Unmeasured counts projections whose savings could not be estimated; they are excluded from saved tokens."})
	}
	return fields
}

// MetricFields projects the same session metrics for terminal and browser panels.
// Source: apps/mekugi/internal/terminalsession/panel_metrics.go@a90c7860.
func MetricFields(body *PanelView, tab, exchange int) (fields []StatusField) {
	add := func(group, label string, value any) {
		text := fmt.Sprint(value)
		if text == "" {
			return
		}
		fields = append(fields, StatusField{Group: group, Label: label, Value: text})
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
	s := *body.Metrics
	if tab == MetricFeatures {
		fields = featureFields(body.Features, s.Schema != "")
		return
	}
	if tab == 0 && body.DebugDirectory != "" {
		add("Debug", "Directory", pathdisplay.ForWorkspace(body.Workspace, body.DebugDirectory))
		fields[len(fields)-1].Path = true
	}
	if tab == 0 && s.StorageWrites != nil {
		add("Storage writes", "Application bytes", s.StorageWrites.Bytes)
		add("Storage writes", "", s.StorageWrites.Scope)
	}
	if s.Schema == "" {
		add("Capture", "", "Metrics are unavailable for this frontend. Live launch metrics are not restored from saved sessions.")
		return
	}
	switch tab {
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
		add("Usage evidence", "", "Attempt coverage: complete categories, partial telemetry, or no usage observed.")
		add("Usage evidence", "Complete", s.Usage.CompleteAttempts)
		add("Usage evidence", "Partial", s.Usage.IncompleteAttempts)
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
		index := max(0, min(exchange, len(s.Exchanges)-1))
		e := s.Exchanges[index]
		add("Exchange", "Retained", fmt.Sprintf("%d / %d", index+1, len(s.Exchanges)))
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
				if a.Usage.CompleteAttempts != 0 {
					add(group, "Usage evidence", "complete")
				} else {
					add(group, "Usage evidence", "partial")
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
	return
}
