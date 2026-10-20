// Source: capturer/ax.go@fdb889f8.
package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"path/filepath"
)

type AXDefectAssessment struct {
	CallID   string `json:"call_id"`
	Verdict  string `json:"verdict"`
	Evidence string `json:"evidence"`
	SHA256   string `json:"sha256"`
}

type AXDefectMetrics struct {
	AssessedCalls uint64               `json:"assessed_calls"`
	Reported      uint64               `json:"reported_defects"`
	Unassessed    uint64               `json:"unassessed_calls"`
	Assessments   []AXDefectAssessment `json:"assessments"`
}

// ReadDefects requires a real evidence artifact for every explicit verdict.
// It records provenance, not a claim that a test failure proves edit causality.
func (service *Service) ReadDefects(path string, editCalls map[string]bool) (AXDefectMetrics, error) {
	result := AXDefectMetrics{Unassessed: uint64(len(editCalls)), Assessments: []AXDefectAssessment{}}
	if path == "" {
		return result, nil
	}
	file, err := openAXEvidence(path, 1<<20)
	if err != nil {
		return result, err
	}
	defer file.Close()
	decoder := jsontext.NewDecoder(io.LimitReader(file, (1<<20)+1), jsonv1.DefaultOptionsV1())
	var assessments []struct {
		CallID   string `json:"call_id"`
		Verdict  string `json:"verdict"`
		Evidence string `json:"evidence"`
	}
	if err := json.UnmarshalDecode(decoder, &assessments, jsonv1.DefaultOptionsV1(), json.RejectUnknownMembers(true)); err != nil {
		return result, errors.New("invalid defect assessment JSON")
	}
	if assessments == nil {
		return result, errors.New("defect assessments must be an array")
	}
	if err := json.UnmarshalDecode(decoder, new(any), jsonv1.DefaultOptionsV1()); !errors.Is(err, io.EOF) {
		return result, errors.New("trailing defect assessment data")
	}
	seen := make(map[string]bool)
	for _, assessment := range assessments {
		if !editCalls[assessment.CallID] || seen[assessment.CallID] ||
			(assessment.Verdict != "defect" && assessment.Verdict != "no_defect") || assessment.Evidence == "" {
			return result, errors.New("defect assessment needs one known edit call, verdict, and evidence path")
		}
		evidencePath := assessment.Evidence
		if !filepath.IsAbs(evidencePath) {
			evidencePath = filepath.Join(filepath.Dir(path), evidencePath)
		}
		evidencePath, err = filepath.Abs(evidencePath)
		if err != nil {
			return result, err
		}
		evidence, err := openAXEvidence(evidencePath, 1<<20)
		if err != nil {
			return result, fmt.Errorf("defect evidence for call %q is unavailable", assessment.CallID)
		}
		data, readErr := io.ReadAll(io.LimitReader(evidence, (1<<20)+1))
		closeErr := evidence.Close()
		if readErr != nil || closeErr != nil || len(data) == 0 || len(data) > 1<<20 {
			return result, errors.New("defect evidence must be nonempty and at most 1 MiB")
		}
		digest := sha256.Sum256(data)
		result.Assessments = append(result.Assessments, AXDefectAssessment{
			CallID: assessment.CallID, Verdict: assessment.Verdict, Evidence: evidencePath, SHA256: hex.EncodeToString(digest[:]),
		})
		seen[assessment.CallID] = true
		result.AssessedCalls++
		result.Unassessed--
		if assessment.Verdict == "defect" {
			result.Reported++
		}
	}
	return result, nil
}
