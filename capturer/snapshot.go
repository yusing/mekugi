package capturer

import (
	"cmp"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"slices"
)

type usageMetrics struct {
	CompleteAttempts    uint64 `json:"complete_attempts"`
	IncompleteAttempts  uint64 `json:"incomplete_attempts"`
	UnknownAttempts     uint64 `json:"unknown_attempts"`
	MissingAttempts     uint64 `json:"missing_attempts"`
	InputTokens         uint64 `json:"input_tokens"`
	CachedInputTokens   uint64 `json:"cached_input_tokens"`
	UncachedInputTokens uint64 `json:"uncached_input_tokens"`
	OutputTokens        uint64 `json:"output_tokens"`
	ReasoningTokens     uint64 `json:"reasoning_tokens"`
	ProviderAttempts    uint64 `json:"provider_attempts"`
}

type requestTotals struct {
	Retries          uint64 `json:"retries"`
	Logical          uint64 `json:"logical"`
	ProviderAttempts uint64 `json:"provider_attempts"`
	Completed        uint64 `json:"completed"`
	Failed           uint64 `json:"failed"`
}

type cacheMetrics struct {
	ProviderCacheRate *float64 `json:"provider_cache_rate"`
}

type payloadTotals struct {
	Bytes  uint64 `json:"bytes"`
	Tokens uint64 `json:"tokens"`
}

type transportMetrics struct {
	ClientControlRequests    payloadTotals `json:"client_control_requests"`
	ClientRequests           payloadTotals `json:"client_requests"`
	ProviderAttemptRequests  payloadTotals `json:"provider_attempt_requests"`
	ProviderControlRequests  payloadTotals `json:"provider_control_requests"`
	ProviderControlResponses payloadTotals `json:"provider_control_responses"`
	ProviderResponses        payloadTotals `json:"provider_responses"`
	ClientControlResponses   payloadTotals `json:"client_control_responses"`
	ClientResponses          payloadTotals `json:"client_responses"`
}

func addWebSocketControl(metrics *transportMetrics, record captureRecord) bool {
	switch {
	case record.Boundary == "codex_control" && record.ControlDirection == ResponsesWebSocketControlRequest:
		addPayload(&metrics.ClientControlRequests, record.Request)
	case record.Boundary == "codex_control" && record.ControlDirection == ResponsesWebSocketControlResponse:
		addPayload(&metrics.ClientControlResponses, record.Response)
	case record.Boundary == "provider_control" && record.ControlDirection == ResponsesWebSocketControlRequest:
		addPayload(&metrics.ProviderControlRequests, record.Request)
	case record.Boundary == "provider_control" && record.ControlDirection == ResponsesWebSocketControlResponse:
		addPayload(&metrics.ProviderControlResponses, record.Response)
	default:
		return false
	}
	return true
}

type semanticOutputMetrics struct {
	ProviderAttemptOutputs payloadTotals `json:"provider_attempt_outputs"`
	ClientOutputs          payloadTotals `json:"client_outputs"`
}

type toolAggregate struct {
	Calls       uint64 `json:"calls"`
	InputBytes  uint64 `json:"input_bytes"`
	InputTokens uint64 `json:"input_tokens"`
	ItemBytes   uint64 `json:"item_bytes"`
	ItemTokens  uint64 `json:"item_tokens"`
}

type captureHealth struct {
	Records                uint64 `json:"records"`
	CaptureErrors          uint64 `json:"capture_errors"`
	Incomplete             uint64 `json:"incomplete_records"`
	MissingProvider        uint64 `json:"missing_provider_records"`
	AttemptGaps            uint64 `json:"provider_attempt_gaps"`
	WriteErrors            uint64 `json:"write_errors"`
	SkippedRequests        uint64 `json:"skipped_requests"`
	DroppedExchangeDetails uint64 `json:"dropped_exchange_details"`
}

type providerAttemptMetrics struct {
	DurationMillis       uint64                    `json:"duration_ms"`
	StatusCode           int                       `json:"status_code"`
	CaptureError         string                    `json:"capture_error,omitempty"`
	Transport            string                    `json:"transport,omitempty"`
	ProviderResponse     *providerResponseEvidence `json:"provider_response,omitempty"`
	Attempt              uint64                    `json:"attempt"`
	Model                string                    `json:"model,omitempty"`
	Status               string                    `json:"status"`
	ResponseComplete     bool                      `json:"response_complete"`
	Usage                *usageMetrics             `json:"usage,omitempty"`
	Request              payloadMetrics            `json:"request"`
	Fingerprint          *requestFingerprint       `json:"cache_fingerprint,omitempty"`
	ProjectedFingerprint *requestFingerprint       `json:"projected_fingerprint,omitempty"`
	ProjectedRequest     *payloadMetrics           `json:"projected_request,omitempty"`
	Response             payloadMetrics            `json:"response"`
	FinalOutput          payloadMetrics            `json:"final_output,omitzero"`
	FinalText            payloadMetrics            `json:"final_text,omitzero"`
	Tools                []toolCallMetrics         `json:"tools,omitempty"`
}

type exchangeMetrics struct {
	StatusCode       int    `json:"status_code"`
	ResponseComplete bool   `json:"response_complete"`
	CaptureError     string `json:"capture_error,omitempty"`
	CompactionMetrics
	Journal             *JournalMetrics          `json:"journal,omitempty"`
	DurationMillis      uint64                   `json:"duration_ms"`
	PredecessorSequence uint64                   `json:"predecessor_sequence,omitempty"`
	RequestKind         string                   `json:"request_kind,omitempty"`
	ClientFingerprint   *requestFingerprint      `json:"client_fingerprint,omitempty"`
	CacheDiagnosis      *cacheDiagnosis          `json:"cache_diagnostics,omitempty"`
	Sequence            uint64                   `json:"sequence"`
	ThreadID            string                   `json:"thread_id,omitempty"`
	Model               string                   `json:"model,omitempty"`
	ProviderAttempts    []providerAttemptMetrics `json:"provider_attempts"`
	Status              string                   `json:"status"`
	Usage               *usageMetrics            `json:"usage,omitempty"`
	ClientRequest       payloadMetrics           `json:"client_request"`
	ClientResponse      payloadMetrics           `json:"client_response"`
	ClientFinalText     payloadMetrics           `json:"client_final_text,omitzero"`
	ClientFinalOutput   payloadMetrics           `json:"client_final_output,omitzero"`
	DeliveredTools      []toolCallMetrics        `json:"delivered_tools,omitempty"`
}

// MetricsSnapshot is a detached, capture-owned metrics view.
type MetricsSnapshot struct {
	Schema         string                   `json:"schema"`
	Mode           string                   `json:"mode"`
	Requests       requestTotals            `json:"requests"`
	Usage          usageMetrics             `json:"usage"`
	Cache          cacheMetrics             `json:"cache"`
	Transport      transportMetrics         `json:"transport"`
	Semantic       semanticOutputMetrics    `json:"semantic"`
	ProviderTools  map[string]toolAggregate `json:"provider_tools"`
	DeliveredTools map[string]toolAggregate `json:"delivered_tools"`
	Exchanges      []exchangeMetrics        `json:"exchanges"`
	Capture        captureHealth            `json:"capture"`
}

// ServeHTTP exposes capture-owned calculations on the router's existing
// listener. It never reads or mutates router state.
func (r *Recorder) ServeHTTP(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	if err := r.WriteMetrics(writer); err != nil {
		return
	}
}

// WriteMetrics exports the same capture-owned snapshot used by the session dialog.
func (r *Recorder) WriteMetrics(writer io.Writer) error {
	return json.NewEncoder(writer).Encode(r.snapshot())
}

// Snapshot returns an independent view safe for presentation without holding recorder locks.
func (r *Recorder) Snapshot() MetricsSnapshot { return r.snapshot() }

func (r *Recorder) snapshot() MetricsSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	snapshot := cloneMetricsSnapshot(r.metrics)
	diagnoseCacheExchanges(snapshot.Exchanges)
	return snapshot
}

func newMetricsSnapshot(mode string) MetricsSnapshot {
	return MetricsSnapshot{
		Schema:         "mekugi.capture.metrics.v7",
		Mode:           mode,
		ProviderTools:  map[string]toolAggregate{},
		DeliveredTools: map[string]toolAggregate{},
	}
}

func (r *Recorder) addExchange(front captureRecord, providers []captureRecord) {
	slices.SortFunc(providers, func(first, second captureRecord) int {
		return cmp.Compare(first.ProviderAttempt, second.ProviderAttempt)
	})
	if len(providers) == 0 && front.ResponseStatus == "completed" && (front.ProviderExpected == nil || *front.ProviderExpected) {
		r.metrics.Capture.MissingProvider++
	}
	for index, provider := range providers {
		if provider.ProviderAttempt != uint64(index+1) {
			r.metrics.Capture.AttemptGaps++
		}
	}

	exchange := exchangeMetrics{
		StatusCode: front.StatusCode, ResponseComplete: front.ResponseComplete, CaptureError: front.CaptureError,
		Journal:           front.Journal.Clone(),
		CompactionMetrics: front.CompactionMetrics,
		DurationMillis:    front.DurationMillis,
		RequestKind:       front.RequestKind,
		Sequence:          front.RequestSequence, ThreadID: front.ThreadID, PredecessorSequence: front.PredecessorSequence,
		Status: front.ResponseStatus, ClientRequest: front.Request, ClientResponse: front.Response,
		ClientFingerprint: front.Fingerprint,
		ClientFinalOutput: front.FinalOutput, ClientFinalText: front.FinalText,
		DeliveredTools: slices.Clone(front.ToolCalls),
	}
	var exchangeUsage usageMetrics
	r.metrics.Requests.Logical++
	r.metrics.Requests.ProviderAttempts += uint64(len(providers))
	r.metrics.Requests.Retries += uint64(max(0, len(providers)-1))
	addPayload(&r.metrics.Transport.ClientRequests, front.Request)
	addPayload(&r.metrics.Transport.ClientResponses, front.Response)
	addPayload(&r.metrics.Semantic.ClientOutputs, front.FinalOutput)
	addTools(r.metrics.DeliveredTools, front.ToolCalls)
	if front.ResponseStatus == "completed" {
		r.metrics.Requests.Completed++
	} else {
		r.metrics.Requests.Failed++
	}
	for _, provider := range providers {
		addPayload(&r.metrics.Transport.ProviderAttemptRequests, provider.Request)
		addPayload(&r.metrics.Transport.ProviderResponses, provider.Response)
		addPayload(&r.metrics.Semantic.ProviderAttemptOutputs, provider.FinalOutput)
		addTools(r.metrics.ProviderTools, provider.ToolCalls)
		attempt := providerAttemptMetrics{
			DurationMillis: provider.DurationMillis, StatusCode: provider.StatusCode, CaptureError: provider.CaptureError,
			Transport:        provider.Transport,
			ProviderResponse: provider.ProviderResponse,
			Attempt:          provider.ProviderAttempt, Model: provider.RequestModel, Status: provider.ResponseStatus,
			ResponseComplete: provider.ResponseComplete,
			Fingerprint:      provider.Fingerprint, ProjectedFingerprint: provider.ProjectedFingerprint,
			ProjectedRequest: provider.ProjectedRequest,
			Request:          provider.Request, Response: provider.Response, FinalOutput: provider.FinalOutput, FinalText: provider.FinalText,
			Tools: slices.Clone(provider.ToolCalls),
		}
		if provider.RequestModel != "" {
			exchange.Model = provider.RequestModel
		}
		if provider.Usage != nil {
			usage := usageOf(*provider.Usage)
			attempt.Usage = &usage
			addUsage(&exchangeUsage, usage)
			addUsage(&r.metrics.Usage, usage)
		} else {
			exchangeUsage.MissingAttempts++
			r.metrics.Usage.MissingAttempts++
		}
		exchange.ProviderAttempts = append(exchange.ProviderAttempts, attempt)
	}
	if exchangeUsage.ProviderAttempts != 0 {
		exchange.Usage = &exchangeUsage
	}
	if front.CompactionAnswer == "router" && len(providers) == 0 {
		exchange.Usage = &usageMetrics{} // Observed no provider invocation, not inferred missing usage.
	}
	if r.metrics.Usage.InputTokens != 0 {
		rate := float64(r.metrics.Usage.CachedInputTokens) / float64(r.metrics.Usage.InputTokens)
		r.metrics.Cache.ProviderCacheRate = &rate
	}
	if len(r.metrics.Exchanges) == maxRetainedExchangeDetails {
		copy(r.metrics.Exchanges, r.metrics.Exchanges[1:])
		r.metrics.Exchanges[len(r.metrics.Exchanges)-1] = exchange
		r.metrics.Capture.DroppedExchangeDetails++
	} else {
		r.metrics.Exchanges = append(r.metrics.Exchanges, exchange)
	}
}

func cloneMetricsSnapshot(source MetricsSnapshot) MetricsSnapshot {
	clone := source
	clone.ProviderTools = maps.Clone(source.ProviderTools)
	clone.DeliveredTools = maps.Clone(source.DeliveredTools)
	if source.Cache.ProviderCacheRate != nil {
		rate := *source.Cache.ProviderCacheRate
		clone.Cache.ProviderCacheRate = &rate
	}
	clone.Exchanges = make([]exchangeMetrics, len(source.Exchanges))
	for index, exchange := range source.Exchanges {
		clone.Exchanges[index] = exchange
		if exchange.CompactionSummaryBytes != nil {
			clone.Exchanges[index].CompactionSummaryBytes = new(*exchange.CompactionSummaryBytes)
		}
		if exchange.CompactionChanges != nil {
			clone.Exchanges[index].CompactionChanges = new(*exchange.CompactionChanges)
		}
		if exchange.CompactionFailures != nil {
			clone.Exchanges[index].CompactionFailures = new(*exchange.CompactionFailures)
		}
		clone.Exchanges[index].Journal = exchange.Journal.Clone()
		clone.Exchanges[index].ClientFingerprint = cloneFingerprint(exchange.ClientFingerprint)
		clone.Exchanges[index].DeliveredTools = slices.Clone(exchange.DeliveredTools)
		clone.Exchanges[index].ProviderAttempts = make([]providerAttemptMetrics, len(exchange.ProviderAttempts))
		for attemptIndex, attempt := range exchange.ProviderAttempts {
			clone.Exchanges[index].ProviderAttempts[attemptIndex] = attempt
			clone.Exchanges[index].ProviderAttempts[attemptIndex].ProviderResponse = cloneProviderEvidence(attempt.ProviderResponse)
			clone.Exchanges[index].ProviderAttempts[attemptIndex].Fingerprint = cloneFingerprint(attempt.Fingerprint)
			clone.Exchanges[index].ProviderAttempts[attemptIndex].ProjectedFingerprint = cloneFingerprint(attempt.ProjectedFingerprint)
			clone.Exchanges[index].ProviderAttempts[attemptIndex].Tools = slices.Clone(attempt.Tools)
			if attempt.ProjectedRequest != nil {
				clone.Exchanges[index].ProviderAttempts[attemptIndex].ProjectedRequest = new(*attempt.ProjectedRequest)
			}
			if attempt.Usage != nil {
				usage := *attempt.Usage
				clone.Exchanges[index].ProviderAttempts[attemptIndex].Usage = &usage
			}
		}
		if exchange.Usage != nil {
			usage := *exchange.Usage
			clone.Exchanges[index].Usage = &usage
		}
	}
	return clone
}

// usageOf converts a ProviderUsage observation to internal usageMetrics.
func usageOf(usage ProviderUsage) usageMetrics {
	cached := min(usage.InputTokens, usage.CachedTokens)
	result := usageMetrics{
		InputTokens: usage.InputTokens, CachedInputTokens: cached,
		UncachedInputTokens: usage.InputTokens - cached,
		OutputTokens:        usage.OutputTokens, ReasoningTokens: usage.ReasoningTokens,
		ProviderAttempts: 1,
	}
	switch {
	case usage.EvidenceComplete == nil:
		result.UnknownAttempts = 1
	case !*usage.EvidenceComplete:
		result.IncompleteAttempts = 1
	default:
		result.CompleteAttempts = 1
	}
	return result
}

func addUsage(total *usageMetrics, usage usageMetrics) {
	total.InputTokens += usage.InputTokens
	total.CachedInputTokens += usage.CachedInputTokens
	total.UncachedInputTokens += usage.UncachedInputTokens
	total.OutputTokens += usage.OutputTokens
	total.ReasoningTokens += usage.ReasoningTokens
	total.ProviderAttempts += usage.ProviderAttempts
	total.CompleteAttempts += usage.CompleteAttempts
	total.IncompleteAttempts += usage.IncompleteAttempts
	total.UnknownAttempts += usage.UnknownAttempts
	total.MissingAttempts += usage.MissingAttempts
}

func addPayload(total *payloadTotals, payload payloadMetrics) {
	total.Bytes += payload.Bytes
	total.Tokens += payload.Tokens
}

func addTools(totals map[string]toolAggregate, calls []toolCallMetrics) {
	for _, call := range calls {
		total := totals[call.Name]
		total.Calls++
		total.InputBytes += call.InputBytes
		total.InputTokens += call.InputTokens
		total.ItemBytes += call.ItemBytes
		total.ItemTokens += call.ItemTokens
		totals[call.Name] = total
	}
}
