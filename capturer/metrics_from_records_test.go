package capturer

import (
	"errors"
)

func metricsFromRecords(mode string, records []captureRecord) (MetricsSnapshot, error) {
	recorder := &Recorder{metrics: newMetricsSnapshot(mode)}
	fronts := make(map[string]captureRecord)
	providers := make(map[string][]captureRecord)
	var order []string
	sequences := make(map[uint64]bool)
	for _, record := range records {
		if record.Boundary == "codex_control" || record.Boundary == "provider_control" {
			if record.CaptureID == "" {
				return MetricsSnapshot{}, errors.New("missing control capture identity")
			}
			if !addWebSocketControl(&recorder.metrics.Transport, record) {
				return MetricsSnapshot{}, errors.New("invalid WebSocket control direction")
			}
			continue
		}
		if record.CaptureID == "" || record.RequestSequence == 0 {
			return MetricsSnapshot{}, errors.New("missing capture identity")
		}
		switch record.Boundary {
		case "codex":
			if _, exists := fronts[record.CaptureID]; exists || sequences[record.RequestSequence] {
				return MetricsSnapshot{}, errors.New("duplicate client capture")
			}
			fronts[record.CaptureID] = record
			sequences[record.RequestSequence] = true
			order = append(order, record.CaptureID)
		case "provider":
			providers[record.CaptureID] = append(providers[record.CaptureID], record)
		default:
			return MetricsSnapshot{}, errors.New("unknown capture boundary")
		}
	}
	for _, id := range order {
		for _, provider := range providers[id] {
			if provider.RequestSequence != fronts[id].RequestSequence {
				return MetricsSnapshot{}, errors.New("provider capture sequence mismatch")
			}
		}
		recorder.addExchange(fronts[id], providers[id])
		delete(providers, id)
	}
	if len(providers) != 0 {
		return MetricsSnapshot{}, errors.New("provider capture has no client")
	}
	recorder.metrics.Capture.Records = uint64(len(records))
	return recorder.snapshot(), nil
}
