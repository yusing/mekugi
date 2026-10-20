package capturer

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"time"
	"unicode/utf8"
)

// UsageInspection keeps provider-consumption evidence separate from local byte
// measurements. Missing/incomplete records never count as zero-token successes.
type UsageInspection struct {
	UnknownCompletenessRecords uint64         `json:"unknown_completeness_records"`
	State                      string         `json:"state"`
	ObservedRecords            uint64         `json:"observed_records"`
	MissingRecords             uint64         `json:"missing_records"`
	Tokens                     *ProviderUsage `json:"tokens,omitempty"`
}

type UsageInspectionFilter struct {
	Since, Until        time.Time
	Model, ExcludeModel string
	Threads             map[string]bool
}

// InspectProviderUsage reads sanitized capture JSONL, never executable history.
func InspectProviderUsage(ctx context.Context, path string, filter UsageInspectionFilter) (map[string]UsageInspection, error) {
	result := make(map[string]UsageInspection)
	file, err := openAXEvidence(path, maxAXEvidenceBytes)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader := &io.LimitedReader{R: file, N: maxAXEvidenceBytes + 1}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 32<<20)
	seen := make(map[string]bool)
	for line := 1; scanner.Scan(); line++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var record captureRecord
		if !utf8.Valid(scanner.Bytes()) || json.Unmarshal(scanner.Bytes(), &record) != nil ||
			record.SchemaVersion != schemaVersion || record.CapturedAt.IsZero() {
			return nil, fmt.Errorf("capture line %d: invalid record or unsupported schema", line)
		}
		if record.Boundary != "provider" {
			continue
		}
		if record.CaptureID == "" || record.ProviderAttempt == 0 {
			return nil, fmt.Errorf("capture line %d: missing provider identity", line)
		}
		key := fmt.Sprintf("%s/%d", record.CaptureID, record.ProviderAttempt)
		if seen[key] {
			return nil, fmt.Errorf("capture line %d: duplicate provider attempt", line)
		}
		seen[key] = true
		modelOK, _ := filepath.Match(filter.Model, record.RequestModel)
		excluded, _ := filepath.Match(filter.ExcludeModel, record.RequestModel)
		if !filter.Threads[record.ThreadID] || record.CapturedAt.Before(filter.Since) || !record.CapturedAt.Before(filter.Until) || !modelOK || excluded {
			continue
		}
		entry := result[record.ThreadID]
		valid := record.Usage != nil && record.ResponseComplete && record.CaptureError == ""
		if record.Usage != nil && record.Usage.EvidenceComplete == nil {
			entry.UnknownCompletenessRecords++
		}
		valid = valid && record.Usage.EvidenceComplete != nil && *record.Usage.EvidenceComplete
		var raw struct {
			Usage map[string]json.RawMessage `json:"usage"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &raw); err != nil {
			return nil, err
		}
		for _, field := range []string{"input_tokens", "cached_input_tokens", "output_tokens", "reasoning_tokens"} {
			if len(raw.Usage[field]) == 0 || string(raw.Usage[field]) == "null" {
				valid = false
			}
		}
		if !valid {
			entry.MissingRecords++
		} else {
			if record.Usage.CachedTokens > record.Usage.InputTokens || record.Usage.ReasoningTokens > record.Usage.OutputTokens {
				return nil, errors.New("invalid provider usage totals")
			}
			if entry.Tokens == nil {
				entry.Tokens = new(ProviderUsage)
			}
			for _, pair := range [][2]*uint64{
				{&entry.Tokens.InputTokens, &record.Usage.InputTokens},
				{&entry.Tokens.CachedTokens, &record.Usage.CachedTokens},
				{&entry.Tokens.OutputTokens, &record.Usage.OutputTokens},
				{&entry.Tokens.ReasoningTokens, &record.Usage.ReasoningTokens},
			} {
				if math.MaxUint64-*pair[0] < *pair[1] {
					return nil, errors.New("provider usage overflow")
				}
				*pair[0] += *pair[1]
			}
			entry.ObservedRecords++
		}
		entry.State = "observed"
		if entry.MissingRecords > 0 {
			entry.State = "incomplete"
		}
		result[record.ThreadID] = entry
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if reader.N == 0 {
		return nil, errors.New("capture exceeds evidence size limit")
	}
	return result, nil
}
