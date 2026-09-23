package capturer

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
)

// MergeSessions combines complete benchmark session exports using the same
// measurement calculations as the live capturer. Original exports are retained;
// only sequence identities in the combined artifacts are rebased.
func MergeSessions(directories []string, metrics, capture io.Writer) error {
	var all []captureRecord
	var mode string
	var offset uint64
	threads := make(map[string]bool)
	for index, directory := range directories {
		encoded, err := os.ReadFile(filepath.Join(directory, "metrics.json"))
		if err != nil {
			return err
		}
		var snapshot metricsSnapshot
		if err := json.Unmarshal(encoded, &snapshot); err != nil {
			return err
		}
		if snapshot.Schema != "mekugi.capture.metrics.v6" {
			return errors.New("unsupported session metrics schema")
		}
		if index == 0 {
			mode = snapshot.Mode
		}
		if snapshot.Mode != mode {
			return errors.New("session modes differ")
		}
		file, err := os.Open(filepath.Join(directory, "capture.jsonl"))
		if err != nil {
			return err
		}
		decoder := json.NewDecoder(file)
		var records []captureRecord
		for {
			var record captureRecord
			err := decoder.Decode(&record)
			if err == io.EOF {
				break
			}
			if err != nil {
				file.Close()
				return err
			}
			if record.SchemaVersion != schemaVersion || record.Mode != mode || record.CaptureError != "" || !record.ResponseComplete {
				file.Close()
				return errors.New("incomplete or incompatible session capture")
			}
			records = append(records, record)
		}
		if err := file.Close(); err != nil {
			return err
		}
		local, err := metricsFromRecords(mode, records)
		if err != nil {
			return err
		}
		// The attribution label is additive; older v4 exports used the same
		// calculation without naming its basis.
		if snapshot.Cache.AttributionBasis == "" {
			snapshot.Cache.AttributionBasis = local.Cache.AttributionBasis
		}
		sortExchanges := func(m *metricsSnapshot) {
			slices.SortFunc(m.Exchanges, func(a, b exchangeMetrics) int { return cmp.Compare(a.Sequence, b.Sequence) })
		}
		sortExchanges(&snapshot)
		sortExchanges(&local)
		want, _ := json.Marshal(snapshot)
		got, _ := json.Marshal(local)
		if !bytes.Equal(want, got) {
			return fmt.Errorf("session capture does not reconcile its metrics: %s", directory)
		}
		sessionThreads := make(map[string]bool)
		var maximum uint64
		for i := range records {
			record := &records[i]
			if record.ThreadID != "" {
				sessionThreads[record.ThreadID] = true
			}
			maximum = max(maximum, record.RequestSequence)
			record.RequestSequence += offset
			if record.PredecessorSequence != 0 {
				record.PredecessorSequence += offset
			}
		}
		for thread := range sessionThreads {
			if threads[thread] {
				return errors.New("benchmark sessions must use distinct threads")
			}
			threads[thread] = true
		}
		offset += maximum
		all = append(all, records...)
	}
	if len(directories) == 0 {
		return errors.New("no session exports")
	}
	result, err := metricsFromRecords(mode, all)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(capture)
	for _, record := range all {
		if err := encoder.Encode(record); err != nil {
			return err
		}
	}
	return json.NewEncoder(metrics).Encode(result)
}

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
