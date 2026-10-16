package router

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yusing/mekugi/backends/codex/rollout"

	"github.com/yusing/mekugi/modules/agentmetrics/evidence"

	"github.com/yusing/mekugi/backends/codex/binding"

	"github.com/yusing/mekugi/modules/sessionhistory/history"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
)

type corpusCallRef struct {
	CallID    string `json:"call_id"`
	Line      int    `json:"line"`
	Timestamp string `json:"timestamp"`
}

type corpusFinding struct {
	Kind      string          `json:"kind"`
	Candidate bool            `json:"candidate"`
	Calls     []corpusCallRef `json:"calls"`
	CallCount int             `json:"call_count"`
}

type corpusSession struct {
	Path              string                           `json:"path"`
	ThreadID          string                           `json:"thread_id"`
	Class             string                           `json:"classification"`
	Models            []string                         `json:"models"`
	SelectedCalls     int                              `json:"selected_calls"`
	MatchedCalls      int                              `json:"matched_replay_calls"`
	UnavailableCalls  int                              `json:"unavailable_replay_calls"`
	MissingTimestamps int                              `json:"missing_timestamps"`
	Findings          []corpusFinding                  `json:"findings"`
	OmittedFindings   int                              `json:"omitted_findings"`
	ProviderUsage     transportcapture.UsageInspection `json:"provider_usage"`
}

type corpusUnavailable struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type sessionCorpus struct {
	Schema       string              `json:"schema"`
	Since        time.Time           `json:"since"`
	Until        time.Time           `json:"until"`
	Model        string              `json:"model"`
	ExcludeModel string              `json:"exclude_model,omitempty"`
	Class        string              `json:"classification_filter"`
	Scanned      int                 `json:"scanned_rollouts"`
	Excluded     map[string]int      `json:"excluded"`
	Sessions     []corpusSession     `json:"sessions"`
	Unavailable  []corpusUnavailable `json:"unavailable"`
	UsageScope   string              `json:"provider_usage_scope"`
}

// RunSessionCorpusInspection is read-only. Counts are per rollout: inherited
// calls in forks are not summed into an invented global workload.
func RunSessionCorpusInspection(service *history.Service, agentMetrics *evidence.Service, diagnostics *transportcapture.Service, ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("inspect-sessions", flag.ContinueOnError)
	flags.SetOutput(stderr)
	directory := flags.String("sessions-dir", "", "Codex rollout directory (default CODEX_HOME/sessions)")
	replay := flags.String("replay-dir", "", "read-only replay store override")
	sinceText := flags.String("since", time.Now().UTC().Add(-48*time.Hour).Format(time.RFC3339Nano), "inclusive RFC3339 start")
	untilText := flags.String("until", time.Now().UTC().Format(time.RFC3339Nano), "exclusive RFC3339 end")
	model := flags.String("model", "*", "model glob, applied to calls")
	exclude := flags.String("exclude-model", "", "exclude entire rollouts containing this model glob")
	class := flags.String("class", "all", "all, production, probe, or unknown (metadata-based candidates)")
	limit := flags.Int("limit", 25, "maximum findings per session (1–500)")
	capture := flags.String("capture", "", "optional sanitized provider capture JSONL")
	flags.Usage = func() { fmt.Fprintln(stderr, "Usage: mekugi inspect-sessions [options]"); flags.PrintDefaults() }
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	since, err1 := time.Parse(time.RFC3339Nano, *sinceText)
	until, err2 := time.Parse(time.RFC3339Nano, *untilText)
	_, err3 := filepath.Match(*model, "")
	_, err4 := filepath.Match(*exclude, "")
	if flags.NArg() != 0 || err1 != nil || err2 != nil || !since.Before(until) || err3 != nil || err4 != nil ||
		*limit < 1 || *limit > 500 || !slices.Contains([]string{"all", "production", "probe", "unknown"}, *class) {
		flags.Usage()
		return 2
	}
	fail := func(err error) int { fmt.Fprintln(stderr, "mekugi inspect-sessions:", err); return 1 }
	if *capture != "" && diagnostics == nil {
		return fail(errors.New("transport diagnostics are unavailable"))
	}

	if *directory == "" {
		codexDirectory := os.Getenv("CODEX_HOME")
		if codexDirectory == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return fail(err)
			}
			codexDirectory = filepath.Join(home, ".codex")
		}
		*directory = filepath.Join(codexDirectory, "sessions")
	}
	root, err := filepath.Abs(*directory)
	if err != nil {
		return fail(err)
	}
	if *replay == "" {
		*replay, err = defaultMekugiReplayDirectory()
		if err != nil {
			return fail(err)
		}
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
		return fail(err)
	}
	result := sessionCorpus{Schema: "mekugi.sessions.v1", Since: since, Until: until, Model: *model, ExcludeModel: *exclude, Class: *class,
		Excluded: map[string]int{}, Sessions: []corpusSession{}, Unavailable: []corpusUnavailable{},
		UsageScope: "provider capture records by captured_at in the selected window and model, for included thread IDs; not inferred from rollout bytes"}
	store := &mekugiReplayStore{publication: service.Attach(*replay), directory: *replay}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		result.Scanned++
		var observations sessionAXInput
		var observer func(rollout.InspectionEvent) error
		if agentMetrics != nil {
			observer = observations.observe
		}
		decoded, err := rollout.Inspect(ctx, path, observer)
		if err != nil {
			result.Unavailable = append(result.Unavailable, corpusUnavailable{path, err.Error()})
			continue
		}
		observations.InspectionMetadata = decoded.InspectionMetadata
		calls := decoded.Calls
		classification := classifyCorpusSession(observations)
		if *class != "all" && *class != classification {
			result.Excluded["classification"]++
			continue
		}
		excluded := false
		for _, m := range observations.Models {
			if ok, _ := filepath.Match(*exclude, m); ok {
				excluded = true
			}
		}
		if excluded {
			result.Excluded["model"]++
			continue
		}
		session, err := inspectCorpusSession(ctx, path, calls, observations, store, since, until, *model, *limit)
		if err != nil {
			result.Unavailable = append(result.Unavailable, corpusUnavailable{path, err.Error()})
			continue
		}
		if session.SelectedCalls == 0 && session.MissingTimestamps == 0 {
			result.Excluded["no_calls_in_window_or_model"]++
			continue
		}
		session.Class = classification
		result.Sessions = append(result.Sessions, session)
	}
	if *capture != "" {
		threads := make(map[string]bool)
		for _, session := range result.Sessions {
			if session.ThreadID != "" {
				threads[session.ThreadID] = true
			}
		}
		usage, err := diagnostics.InspectProviderUsage(ctx, *capture, transportcapture.UsageInspectionFilter{
			Since: since, Until: until, Model: *model, ExcludeModel: *exclude, Threads: threads})
		if err != nil {
			return fail(err)
		}
		for i := range result.Sessions {
			if observed, ok := usage[result.Sessions[i].ThreadID]; ok {
				result.Sessions[i].ProviderUsage = observed
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(result); err != nil {
		return fail(err)
	}
	return 0
}

func classifyCorpusSession(observation sessionAXInput) string {
	var source string
	_ = json.Unmarshal(observation.Source, &source)
	cwd, temporary := filepath.Clean(observation.Cwd), filepath.Clean(os.TempDir())
	if source == "exec" || cwd == temporary || strings.HasPrefix(cwd, temporary+string(filepath.Separator)) {
		return "probe"
	}
	if source == "cli" {
		return "production"
	}
	var child struct {
		Subagent json.RawMessage `json:"subagent"`
	}
	if json.Unmarshal(observation.Source, &child) == nil && len(child.Subagent) > 0 {
		return "production"
	}
	return "unknown"
}

func inspectCorpusSession(ctx context.Context, path string, calls []rollout.Call, observation sessionAXInput, store *mekugiReplayStore, since, until time.Time, model string, limit int) (corpusSession, error) {
	session := corpusSession{Path: path, ThreadID: observation.ThreadID, Models: observation.Models, Findings: []corpusFinding{},
		ProviderUsage: transportcapture.UsageInspection{State: "unavailable"}}
	var findings []corpusFinding
	var previous *rollout.Call
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
		root, err := canonicalInspectionWorkspace(call.Workspace)
		if err != nil {
			return session, err
		}
		var record replayRecord
		found := false
		if root != "" {
			record, found, err = store.read(root, call.Item.CallID, false)
			if err != nil {
				return session, err
			}
		}
		_, err = inspectSessionCall(call, record, found, nil, nil, "", 1)
		if err != nil {
			return session, err
		}
		if found {
			session.MatchedCalls++
		} else {
			session.UnavailableCalls++
		}
		ref := corpusCallRef{call.Item.CallID, call.Line, call.Timestamp}
		if inspectionEmptyPoll(call, record.History, found) {
			findings = append(findings, corpusFinding{Kind: "empty_poll", Calls: []corpusCallRef{ref}, CallCount: 1})
		}
		reads := corpusReadSelectionsForCall(call)
		if previous != nil && previous.Workspace == call.Workspace && corpusReadOverlap(previousReads, reads) {
			findings = append(findings, corpusFinding{Kind: "truncation_reread", Candidate: true, CallCount: 2,
				Calls: []corpusCallRef{{previous.Item.CallID, previous.Line, previous.Timestamp}, ref}})
		}
		previous = nil
		if len(reads) > 0 && inspectionHasReadContinuation(call) {
			copy := call
			previous = &copy
			previousReads = reads
		}
	}
	for _, finding := range findings {
		if len(session.Findings) < limit {
			session.Findings = append(session.Findings, finding)
		} else {
			session.OmittedFindings++
		}
	}
	return session, nil
}

func inspectionEmptyPoll(call rollout.Call, history mekugiHistory, found bool) bool {
	if len(call.Outputs) == 0 {
		return false
	}
	var item map[string]json.RawMessage
	if json.Unmarshal(responses.MustMarshalJSON(call.Item), &item) != nil {
		return false
	}
	if !inspectionEmptyInput(item) {
		return false
	}
	classified := binding.ClassifyExecutionCall(item, history.executionEvidence(found), "exec")
	texts := binding.ExecutionOutputTexts(call.Outputs[len(call.Outputs)-1].Output)
	if len(texts) == 0 {
		return false
	}
	var session int64
	if classified.Native {
		session = binding.NativeExecutionSession(texts[0])
	} else if classified.CodeMode {
		state, _, body := binding.CodeModeExecutionHeader(texts[0])
		if state == "" || state == "running" || state == "Script terminated" {
			return false
		}
		texts[0] = body
		for _, text := range texts {
			if id := binding.NativeJSONSession(text); id != 0 {
				if session != 0 {
					return false
				}
				session = id
			}
		}
	}
	if session == 0 {
		return false
	}
	return inspectionEmptyOutput(classified, texts, session)
}

// Empty-poll recognition belongs to offline evidence analysis, not continuation policy.
func inspectionEmptyInput(item map[string]json.RawMessage) bool {
	if namespace := responses.StringField(item, "namespace"); namespace != "" && namespace != "functions" {
		return false
	}
	name := strings.TrimPrefix(responses.StringField(item, "name"), "functions.")
	if name == "exec" {
		source := responses.StringField(item, "input")
		nested, ok := binding.SingleExecutionCall(source, false)
		if !ok {
			return false
		}
		item, name = nested, responses.StringField(nested, "name")
	}
	var args struct {
		Chars string `json:"chars"`
	}
	return name == "write_stdin" && json.Unmarshal([]byte(responses.StringField(item, "arguments")), &args) == nil && args.Chars == ""
}

func inspectionEmptyOutput(call binding.ExecutionCall, texts []string, session int64) bool {
	if call.ResumeHandle != "session:"+strconv.FormatInt(session, 10) {
		return false
	}
	if call.Native {
		_, body := binding.NativeExecutionHeader(texts[0])
		return body == "" && binding.NativeExecutionSession(texts[0]) == session
	}
	// Only transparent native output is evidence, not unrelated printed text.
	found := false
	for _, text := range texts {
		if text == "" {
			continue
		}
		part := responses.MustMarshalJSON(map[string]string{"type": "input_text", "text": text})
		if binding.ExecutionAnnotationMatches(part, map[string]any{"session_id": session}) {
			continue
		}
		var output struct {
			Output *string `json:"output"`
		}
		if binding.NativeJSONSession(text) != session || json.Unmarshal([]byte(text), &output) != nil ||
			output.Output == nil || *output.Output != "" || found {
			return false
		}
		found = true
	}
	return found
}

func inspectionHasReadContinuation(call rollout.Call) bool {
	for _, output := range call.Outputs {
		if strings.Contains(string(output.Output), "read: incomplete; next_call: mread ") {
			return true
		}
	}
	return false
}
