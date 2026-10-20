package preferences

import "time"

// Source: internal/router/app_server_panes.go:31:64@8bcb5cbf nativeAppliedSettings, nativeResumeEvidence

// Applied is a complete host-confirmed snapshot. Empty effort/tier select the
// host default; Model identifies the presence of the snapshot.
type Applied struct {
	Model    string    `json:"model"`
	Effort   string    `json:"effort"`
	Tier     string    `json:"tier"`
	Observed time.Time `json:"observed"`
}

// Evidence preserves partial history: older turn contexts do not identify tier.
type Evidence struct {
	Model     string `json:"model"`
	Effort    string `json:"effort"`
	Tier      string `json:"tier"`
	TierKnown bool   `json:"tierKnown"`
}

// WithNewer combines newer decoded host facts without erasing a known tier when
// the host supplies only a model/effort turn context.
func (s Evidence) WithNewer(newer Evidence) Evidence {
	if newer.Model != "" {
		s.Model, s.Effort = newer.Model, newer.Effort
	}
	if newer.TierKnown {
		s.Tier, s.TierKnown = newer.Tier, true
	}
	return s
}

// ResumeBaseline supplies the retained evidence and the cutoff for host history.
// An interrupted default restore excludes its transient resume checkpoint.
// Source: internal/router/app_server_resume.go:54:79@8bcb5cbf resumeSettingsResponse
func (s Snapshot) ResumeBaseline() (Evidence, time.Time) {
	if s.ResumeEvidence.Model != "" && s.ResumeObserved.After(s.Settings.Observed) {
		return s.ResumeEvidence, s.ResumeObserved
	}
	return Evidence{Model: s.Settings.Model, Effort: s.Settings.Effort, Tier: s.Settings.Tier, TierKnown: s.Settings.Model != ""}, s.Settings.Observed
}
