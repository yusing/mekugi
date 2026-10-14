package orchestrate

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

type WorkflowKind string

const (
	WorkflowImplementation WorkflowKind = "implementation"
	WorkflowDiagnosis      WorkflowKind = "diagnosis"
)

type WorkflowPhase string

const (
	PhaseImplementation WorkflowPhase = "implementation"
	PhaseValidation     WorkflowPhase = "validation"
	PhaseQueuedReview   WorkflowPhase = "queued_review"
	PhaseReview         WorkflowPhase = "review"
	PhaseCorrection     WorkflowPhase = "correction"
	PhaseReady          WorkflowPhase = "ready"
	PhaseNeedsDecision  WorkflowPhase = "needs_decision"
	PhaseCanceled       WorkflowPhase = "canceled"
	PhaseFailed         WorkflowPhase = "failed"
)

type ReviewPurpose string

const (
	ReviewCorrectness    ReviewPurpose = "correctness"
	ReviewSimplification ReviewPurpose = "simplification"
)

type ReviewVerdict string

const (
	VerdictClear    ReviewVerdict = "clear"
	VerdictFindings ReviewVerdict = "findings"
	VerdictBlocked  ReviewVerdict = "blocked"
)

type CheckOutcome string

const (
	CheckPassed    CheckOutcome = "passed"
	CheckFailed    CheckOutcome = "failed"
	CheckUncertain CheckOutcome = "uncertain"
)

// WorkflowAssignment is immutable after configuration, before launch. Diagnosis
// permissions remain the runtime's responsibility; diagnosis cannot be integrated.
type WorkflowAssignment struct {
	Kind           WorkflowKind    `json:"kind"`
	Scope          string          `json:"scope"`
	RequiredChecks []RequiredCheck `json:"required_checks"`
}
type RequiredCheck struct {
	ID      string `json:"id"`
	Command string `json:"command"`
}

// Candidate names the host-observed immutable Git commit in the owned checkout.
// SVN and unversioned batches use their existing private Git snapshot commits.
type Candidate struct {
	Revision string `json:"revision"`
}
type HostReceipt struct {
	ThreadID string `json:"thread_id"`
	TurnID   string `json:"turn_id"`
	ItemID   string `json:"item_id"`
}

// CheckResult is admitted only from the runtime's own host command execution,
// never from an implementer message. An absent exit status cannot mean success.
// Output keeps a bounded tail for review and correction assignments.
type CheckResult struct {
	Generation int           `json:"generation"`
	Phase      WorkflowPhase `json:"phase"`
	Command    string        `json:"command"`
	Cwd        string        `json:"cwd"`
	Revision   string        `json:"revision"`
	CheckID    string        `json:"check_id"`
	Outcome    CheckOutcome  `json:"outcome"`
	ExitCode   *int          `json:"exit_code"`
	Output     string        `json:"output,omitempty"`
}
type ReviewFinding struct {
	ID     string `json:"id"`
	Detail string `json:"detail"`
}
type ReviewResult struct {
	Revision string          `json:"revision"`
	Purpose  ReviewPurpose   `json:"purpose"`
	Verdict  ReviewVerdict   `json:"verdict"`
	Findings []ReviewFinding `json:"findings"`
	Reason   string          `json:"reason"`
}

// PreviousReview preserves one purpose's verdict and its exact host dispatch.
type PreviousReview struct {
	DispatchID string       `json:"dispatch_id"`
	Result     ReviewResult `json:"result"`
}

// ReviewResultSchema constrains a generic read-only host turn. DecodeReviewResult
// also checks cross-field meaning; the schema alone is not an approval gate.
const ReviewResultSchema = `{"type":"object","additionalProperties":false,"required":["revision","purpose","verdict","findings","reason"],"properties":{"revision":{"type":"string","pattern":"^([0-9a-f]{40}|[0-9a-f]{64})$"},"purpose":{"type":"string","enum":["correctness","simplification"]},"verdict":{"type":"string","enum":["clear","findings","blocked"],"description":"clear requires empty findings and reason. findings requires nonempty findings and an empty reason. blocked requires empty findings and a nonblank reason."},"findings":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["id","detail"],"properties":{"id":{"type":"string","minLength":1},"detail":{"type":"string","minLength":1}}}},"reason":{"type":"string","description":"Blocker only. Must be empty for clear or findings. Do not put summaries or validation notes here."}}}`

var revisionPattern = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

func (c Candidate) Validate() error {
	if !revisionPattern.MatchString(c.Revision) {
		return errors.New("candidate requires a full immutable commit identity")
	}
	return nil
}
func (h HostReceipt) valid() bool {
	return strings.TrimSpace(h.ThreadID) != "" && strings.TrimSpace(h.TurnID) != "" && strings.TrimSpace(h.ItemID) != ""
}
func (r CheckResult) Validate() error {
	if err := (Candidate{r.Revision}).Validate(); err != nil {
		return err
	}
	if r.Generation < 1 || r.Phase != PhaseValidation || strings.TrimSpace(r.Command) == "" || strings.TrimSpace(r.Cwd) == "" || strings.TrimSpace(r.CheckID) == "" {
		return errors.New("check requires its declared identity")
	}
	switch r.Outcome {
	case CheckPassed:
		if r.ExitCode == nil || *r.ExitCode != 0 {
			return errors.New("passed check requires host exit status zero")
		}
	case CheckFailed:
		if r.ExitCode == nil || *r.ExitCode == 0 {
			return errors.New("failed check requires nonzero host exit status")
		}
	case CheckUncertain:
		if r.ExitCode != nil {
			return errors.New("uncertain check cannot claim an exit status")
		}
	default:
		return errors.New("invalid check outcome")
	}
	return nil
}
func (r ReviewResult) Validate() error {
	if err := (Candidate{r.Revision}).Validate(); err != nil {
		return err
	}
	if r.Purpose != ReviewCorrectness && r.Purpose != ReviewSimplification {
		return errors.New("invalid review purpose")
	}
	if r.Findings == nil {
		return errors.New("review requires an explicit findings array")
	}
	switch r.Verdict {
	case VerdictClear:
		if len(r.Findings) != 0 || r.Reason != "" {
			return errors.New("clear review cannot contain findings or a blocker")
		}
	case VerdictFindings:
		if len(r.Findings) == 0 || r.Reason != "" {
			return errors.New("findings verdict requires findings without a blocker")
		}
	case VerdictBlocked:
		if strings.TrimSpace(r.Reason) == "" || len(r.Findings) != 0 {
			return errors.New("blocked review requires a reason without findings")
		}
	default:
		return errors.New("invalid review verdict")
	}
	seen := map[string]bool{}
	for _, f := range r.Findings {
		if strings.TrimSpace(f.ID) == "" || strings.TrimSpace(f.Detail) == "" || seen[f.ID] {
			return errors.New("findings require unique stable identities and details")
		}
		seen[f.ID] = true
	}
	return nil
}
func DecodeCandidate(data []byte) (Candidate, error) {
	var v Candidate
	err := json.Unmarshal(data, &v, json.RejectUnknownMembers(true))
	if err == nil {
		err = v.Validate()
	}
	return v, err
}
func DecodeReviewResult(data []byte) (ReviewResult, error) {
	var v ReviewResult
	// Require every schema field even when its valid value is empty.
	var fields map[string]jsontext.Value
	if err := json.Unmarshal(data, &fields); err != nil {
		return v, err
	}
	for _, name := range []string{"revision", "purpose", "verdict", "findings", "reason"} {
		if raw, ok := fields[name]; !ok || string(raw) == "null" {
			return v, fmt.Errorf("review requires %s", name)
		}
	}
	err := json.Unmarshal(data, &v, json.RejectUnknownMembers(true))
	if err == nil {
		err = v.Validate()
	}
	return v, err
}
