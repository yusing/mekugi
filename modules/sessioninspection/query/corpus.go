// Source: internal/router/session_corpus.go@[d9f28a74] offline corpus queries.
package query

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/contracts/inspection"
	"github.com/yusing/mekugi/libraries/legacycall"
)

type CallRef struct {
	CallID    string `json:"call_id"`
	Line      int    `json:"line"`
	Timestamp string `json:"timestamp"`
}

type Finding struct {
	Kind      string    `json:"kind"`
	Candidate bool      `json:"candidate"`
	Calls     []CallRef `json:"calls"`
	CallCount int       `json:"call_count"`
}

type CorpusSession struct {
	Path              string           `json:"path"`
	ThreadID          string           `json:"thread_id"`
	Class             string           `json:"classification"`
	Models            []string         `json:"models"`
	SelectedCalls     int              `json:"selected_calls"`
	MatchedCalls      int              `json:"matched_replay_calls"`
	UnavailableCalls  int              `json:"unavailable_replay_calls"`
	MissingTimestamps int              `json:"missing_timestamps"`
	Findings          []Finding        `json:"findings"`
	OmittedFindings   int              `json:"omitted_findings"`
	ProviderUsage     inspection.Usage `json:"provider_usage"`
}

type Unavailable struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type Corpus struct {
	Schema       string          `json:"schema"`
	Since        time.Time       `json:"since"`
	Until        time.Time       `json:"until"`
	Model        string          `json:"model"`
	ExcludeModel string          `json:"exclude_model,omitempty"`
	Class        string          `json:"classification_filter"`
	Scanned      int             `json:"scanned_rollouts"`
	Excluded     map[string]int  `json:"excluded"`
	Sessions     []CorpusSession `json:"sessions"`
	Unavailable  []Unavailable   `json:"unavailable"`
	UsageScope   string          `json:"provider_usage_scope"`
}

type CorpusOptions struct {
	Directory, ReplayDirectory, Model, ExcludeModel, Class, Capture string
	Since, Until                                                    time.Time
	Limit                                                           int
}

func (s *Service) Corpus(ctx context.Context, options CorpusOptions) (Corpus, error) {
	if s == nil || s.source == nil {
		return Corpus{}, errors.New("inspection source is unavailable")
	}
	if options.Capture != "" && !s.HasProviderUsage() {
		return Corpus{}, errors.New("transport diagnostics are unavailable")
	}
	_, modelErr := filepath.Match(options.Model, "")
	_, excludeErr := filepath.Match(options.ExcludeModel, "")
	if options.Directory == "" || !options.Since.Before(options.Until) || options.Limit < 1 || options.Limit > 500 || modelErr != nil || excludeErr != nil ||
		!slices.Contains([]string{"all", "production", "probe", "unknown"}, options.Class) {
		return Corpus{}, errors.New("invalid corpus options")
	}
	since, until := options.Since, options.Until
	root, err := filepath.Abs(options.Directory)
	if err != nil {
		return Corpus{}, err
	}
	var paths []string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".jsonl") {
			if len(paths) == 10000 {
				return errors.New("corpus exceeds 10000 rollouts; select a narrower sessions directory")
			}
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return Corpus{}, err
	}
	result := Corpus{Schema: "mekugi.sessions.v1", Since: since, Until: until, Model: options.Model, ExcludeModel: options.ExcludeModel, Class: options.Class,
		Excluded: map[string]int{}, Sessions: []CorpusSession{}, Unavailable: []Unavailable{},
		UsageScope: "provider capture records by captured_at in the selected window and model, for included thread IDs; not inferred from rollout bytes"}

	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return Corpus{}, err
		}
		result.Scanned++
		var observer func(inspection.Event) error
		if s.HasAX() {
			observer = s.contributions.NewAX().Observe
		}
		observations, err := s.source(ctx, path, observer)
		if err != nil {
			result.Unavailable = append(result.Unavailable, Unavailable{path, err.Error()})
			continue
		}
		calls := observations.Calls
		classification := classifyCorpusSession(observations)
		if options.Class != "all" && options.Class != classification {
			result.Excluded["classification"]++
			continue
		}
		excluded := false
		for _, m := range observations.Models {
			if ok, _ := filepath.Match(options.ExcludeModel, m); ok {
				excluded = true
			}
		}
		if excluded {
			result.Excluded["model"]++
			continue
		}
		session, err := inspectCorpusSession(ctx, path, calls, observations, options.ReplayDirectory, since, until, options.Model, options.Limit)
		if err != nil {
			result.Unavailable = append(result.Unavailable, Unavailable{path, err.Error()})
			continue
		}
		if session.SelectedCalls == 0 && session.MissingTimestamps == 0 {
			result.Excluded["no_calls_in_window_or_model"]++
			continue
		}
		session.Class = classification
		result.Sessions = append(result.Sessions, session)
	}
	if options.Capture != "" {
		threads := make(map[string]bool)
		for _, session := range result.Sessions {
			if session.ThreadID != "" {
				threads[session.ThreadID] = true
			}
		}
		usage, err := s.contributions.ProviderUsage(ctx, options.Capture, inspection.UsageFilter{
			Since: since, Until: until, Model: options.Model, ExcludeModel: options.ExcludeModel, Threads: threads})
		if err != nil {
			return Corpus{}, err
		}
		for i := range result.Sessions {
			if observed, ok := usage[result.Sessions[i].ThreadID]; ok {
				result.Sessions[i].ProviderUsage = observed
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return Corpus{}, err
	}
	return result, nil
}
func classifyCorpusSession(observation Rollout) string {
	cwd, temporary := filepath.Clean(observation.Cwd), filepath.Clean(os.TempDir())
	if observation.Origin == "batch" || cwd == temporary || strings.HasPrefix(cwd, temporary+string(filepath.Separator)) {
		return "probe"
	}
	if observation.Origin == "interactive" || observation.Origin == "child" {
		return "production"
	}
	return "unknown"
}
func inspectCorpusSession(ctx context.Context, path string, calls []Call, observation Rollout, replayDirectory string, since, until time.Time, model string, limit int) (CorpusSession, error) {
	session := CorpusSession{Path: path, ThreadID: observation.ThreadID, Models: observation.Models, Findings: []Finding{},
		ProviderUsage: inspection.Usage{State: "unavailable"}}
	admit := func(finding Finding) {
		if len(session.Findings) < limit {
			session.Findings = append(session.Findings, finding)
		} else {
			session.OmittedFindings++
		}
	}
	var previous *Call
	var previousReads []corpusReadSelection
	for _, call := range calls {
		if err := ctx.Err(); err != nil {
			return session, err
		}
		at, err := time.Parse(time.RFC3339Nano, call.Timestamp)
		if err != nil {
			session.MissingTimestamps++
			previous = nil
			continue
		}
		modelOK, _ := filepath.Match(model, call.Model)
		if at.Before(since) || !at.Before(until) || !modelOK {
			previous = nil
			continue
		}
		session.SelectedCalls++
		root, err := Workspace(call.Workspace)
		if err != nil {
			return session, err
		}
		var record legacycall.Record
		found := false
		if root != "" {
			record, found, err = legacycall.Read(replayDirectory, root, call.ID, false, 32<<20)
			if err != nil {
				return session, err
			}
		}
		if found {
			if err := call.Evidence.Verify(record.History); err != nil {
				return session, err
			}
			session.MatchedCalls++
		} else {
			session.UnavailableCalls++
		}
		ref := CallRef{call.ID, call.Line, call.Timestamp}
		var retained *legacycall.History
		if found {
			retained = &record.History
		}
		if call.Evidence.EmptyPoll(retained) {
			admit(Finding{Kind: "empty_poll", Calls: []CallRef{ref}, CallCount: 1})
		}
		var reads []corpusReadSelection
		for _, command := range call.Evidence.Commands() {
			reads = append(reads, corpusReadSelections(command)...)
		}
		if previous != nil && previous.Workspace == call.Workspace && corpusReadOverlap(previousReads, reads) {
			admit(Finding{Kind: "truncation_reread", Candidate: true, CallCount: 2,
				Calls: []CallRef{{previous.ID, previous.Line, previous.Timestamp}, ref}})
		}
		previous = nil
		if len(reads) > 0 && call.ReadContinuation {
			copy := call
			previous = &copy
			previousReads = reads
		}
	}
	return session, nil
}
