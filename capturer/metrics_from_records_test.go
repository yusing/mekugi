package capturer

import (
	"cmp"
	"errors"
	"slices"
)

func metricsFromRecords(mode string, records []captureRecord) (metricsSnapshot, error) {
	recorder := &Recorder{metrics: newMetricsSnapshot(mode), previousInput: make(map[string]uint64), cacheQueues: make(map[string][]*requestState)}
	fronts := make(map[string]captureRecord)
	providers := make(map[string][]captureRecord)
	var order []string
	sequences := make(map[uint64]bool)
	for _, record := range records {
		if record.Boundary == "codex_control" || record.Boundary == "provider_control" {
			if record.CaptureID == "" {
				return metricsSnapshot{}, errors.New("missing control capture identity")
			}
			if !addWebSocketControl(&recorder.metrics.Transport, record) {
				return metricsSnapshot{}, errors.New("invalid WebSocket control direction")
			}
			continue
		}
		if record.CaptureID == "" || record.RequestSequence == 0 {
			return metricsSnapshot{}, errors.New("missing capture identity")
		}
		switch record.Boundary {
		case "codex":
			if _, exists := fronts[record.CaptureID]; exists || sequences[record.RequestSequence] {
				return metricsSnapshot{}, errors.New("duplicate client capture")
			}
			fronts[record.CaptureID] = record
			sequences[record.RequestSequence] = true
			order = append(order, record.CaptureID)
		case "provider":
			providers[record.CaptureID] = append(providers[record.CaptureID], record)
		default:
			return metricsSnapshot{}, errors.New("unknown capture boundary")
		}
	}
	arrival := slices.Clone(order)
	slices.SortFunc(arrival, func(a, b string) int { return cmp.Compare(fronts[a].RequestSequence, fronts[b].RequestSequence) })
	states := make(map[string]*requestState)
	for _, id := range arrival {
		front := fronts[id]
		state := &requestState{threadID: front.ThreadID}
		states[id] = state
		if front.ThreadID != "" {
			recorder.cacheQueues[front.ThreadID] = append(recorder.cacheQueues[front.ThreadID], state)
		}
	}
	for _, id := range order {
		for _, provider := range providers[id] {
			if provider.RequestSequence != fronts[id].RequestSequence {
				return metricsSnapshot{}, errors.New("provider capture sequence mismatch")
			}
		}
		recorder.addExchange(fronts[id], states[id], providers[id])
		delete(providers, id)
	}
	if len(providers) != 0 {
		return metricsSnapshot{}, errors.New("provider capture has no client")
	}
	recorder.metrics.Capture.Records = uint64(len(records))
	return recorder.snapshot(), nil
}
