package sessioncontrol

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"math"
	"math/bits"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/backends/claude"
	"github.com/yusing/mekugi/contracts/inference"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
	"github.com/yusing/mekugi/modules/usage/accounting"
)

// The SDK snapshot is cumulative, including restored and fork-source spend.
// Keep its baseline distinct from this thread's canonical accounting.
type claudeUsage struct {
	inference.Usage
	Cost   *float64
	Models string
}

type claudeMetrics struct {
	Lifecycle                    activity.AgentLifecycle
	Native, Baseline             claudeUsage
	BaselineSet                  bool
	Partial, CompactionsKnown    bool
	Roundtrips                   uint64
	LastResult                   string
	LastMessage, LiveTurn        string
	LiveUsageMessage             string
	LiveRoundtrips               uint64
	ContextTokens, ContextWindow *uint64
	Compactions                  []string
}

func (m *claudeMetrics) inspection() control.SessionMetrics {
	if m == nil {
		return control.SessionMetrics{}
	}
	metrics := control.SessionMetrics{}
	if m.ContextTokens != nil {
		metrics.ContextTokens = new(*m.ContextTokens)
	}
	if m.ContextWindow != nil {
		metrics.ContextWindow = new(*m.ContextWindow)
	}
	if m.CompactionsKnown {
		metrics.Compactions = new(uint64(len(m.Compactions)))
	}
	return metrics
}

// Metrics borrows no transcript and performs no native request or storage read.
func (r *ClaudeRuntime) Metrics() (activity.AgentFacts, accounting.Report) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.ledger.Metrics
	lifecycle := m.Lifecycle
	facts := activity.AgentFacts{Started: lifecycle.Started, LastResponse: lifecycle.LastResponse,
		Turns: lifecycle.Turns, WorkElapsed: time.Duration(lifecycle.WorkTimer.ElapsedNS), WorkSince: lifecycle.WorkTimer.Since, WorkKnown: lifecycle.WorkTimer.Known}
	if m.ContextTokens != nil {
		facts.ContextTokens, facts.ContextKnown = *m.ContextTokens, true
	}
	if m.ContextWindow != nil {
		facts.ContextWindow = *m.ContextWindow
	}
	report := accounting.Report{Roundtrips: m.Roundtrips + m.LiveRoundtrips, PriorUnknown: m.Partial, Model: m.Native.Models}
	if !m.Native.TotalsKnown || !m.BaselineSet {
		report.Incomplete = true
		report.MissingUsage = report.Roundtrips
		return facts, report
	}
	report.Usage = m.Native.Usage
	report.Incomplete = report.Incomplete || m.LiveRoundtrips > 0 && m.LiveUsageMessage != m.LastMessage
	for _, pair := range []struct {
		value    *uint64
		baseline uint64
	}{
		{&report.InputTokens, m.Baseline.InputTokens}, {&report.UncachedInputTokens, m.Baseline.UncachedInputTokens},
		{&report.CacheWriteTokens, m.Baseline.CacheWriteTokens}, {&report.OutputTokens, m.Baseline.OutputTokens},
		{&report.ReasoningTokens, m.Baseline.ReasoningTokens},
	} {
		if *pair.value < pair.baseline {
			report.Usage = inference.Usage{Incomplete: true}
			report.MissingUsage, report.PriorUnknown = report.Roundtrips, true
			return facts, report
		}
		*pair.value -= pair.baseline
	}
	if m.Native.Cost != nil && m.Baseline.Cost != nil && *m.Native.Cost >= *m.Baseline.Cost {
		report.Cost = accounting.Cost{Reported: *m.Native.Cost - *m.Baseline.Cost, Known: true}
	} else if m.Native.Cost != nil && m.Baseline.Cost == nil && r.ledger.Info.Fork == nil {
		report.Cost = accounting.Cost{Reported: *m.Native.Cost, Known: true}
	}
	return facts, report
}

// ObserveMetrics accepts only admitted native results and explicitly sourced
// adapter context observations. Auxiliary persistence errors do not settle work.
func (r *ClaudeRuntime) ObserveMetrics(ctx context.Context, raw jsontext.Value) error {
	var event struct {
		claude.Event
		ResultUUID string  `json:"result_uuid"`
		Tokens     *uint64 `json:"context_tokens"`
		Window     *uint64 `json:"context_window"`
		MessageID  string  `json:"message_id"`
		ParentTool *string `json:"parent_tool_use_id"`
		Error      string  `json:"error"`
		Message    struct {
			ID string `json:"id"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if event.SessionID != r.ledger.Info.Session {
		return nil
	}
	m := r.ledger.Metrics
	switch {
	case event.Type == "assistant" && r.running && event.ParentTool == nil && event.Error == "" && event.Message.ID != "" && event.Message.ID != m.LastMessage:
		if m.LiveTurn != r.turn {
			m.LiveTurn, m.LiveRoundtrips = r.turn, 0
		}
		m.LastMessage = event.Message.ID
		m.LiveRoundtrips++
	case event.Type == "mekugi_live_metrics" && r.running && m.LiveTurn == r.turn && event.MessageID != "" && event.MessageID == m.LastMessage:
		m.ContextTokens, m.ContextWindow = event.Tokens, event.Window
		if m.ContextWindow != nil && *m.ContextWindow == 0 {
			m.ContextWindow = nil
		}
		if usage, err := claudeReportedUsage(raw); err == nil && (!m.Native.TotalsKnown || usage.InputTokens >= m.Native.InputTokens && usage.OutputTokens >= m.Native.OutputTokens) {
			if !m.BaselineSet {
				m.Baseline, m.BaselineSet, m.Partial = usage, true, true
			}
			m.Native = usage
			m.LiveUsageMessage = event.MessageID
		}
	case event.Type == "mekugi_usage_baseline" && r.ledger.Info.Fork != nil && !m.BaselineSet:
		baseline, err := claudeReportedUsage(raw)
		if err != nil {
			m.Partial = true
			break
		}
		m.Baseline, m.BaselineSet = baseline, true
	case event.Type == "mekugi_context_usage" && event.ResultUUID != "" && event.ResultUUID == m.LastResult:
		m.ContextTokens, m.ContextWindow = event.Tokens, event.Window
		if m.ContextWindow != nil && *m.ContextWindow == 0 {
			m.ContextWindow = nil
		}
	case event.Type == "system" && event.Subtype == "compact_boundary" && event.UUID != "":
		if slices.Contains(m.Compactions, event.UUID) {
			return nil
		}
		m.Compactions = append(m.Compactions, event.UUID)
		m.ContextTokens = nil
	case event.Type == "result" && event.UUID == r.metricResult && event.UUID != m.LastResult:
		m.LastResult = event.UUID
		if event.NumTurns != nil && *event.NumTurns == 0 {
			break
		}
		m.LiveRoundtrips = 0
		m.ContextTokens, m.ContextWindow = nil, nil
		if event.NumTurns == nil || *event.NumTurns < 0 {
			m.Partial = true
			break
		}
		rounds, overflow := bits.Add64(m.Roundtrips, uint64(*event.NumTurns), 0)
		if overflow == 0 {
			m.Roundtrips = rounds
		} else {
			m.Partial = true
		}
		usage, err := claudeReportedUsage(raw)
		if err != nil || m.Native.TotalsKnown && (usage.InputTokens < m.Native.InputTokens || usage.OutputTokens < m.Native.OutputTokens) {
			m.Partial = true
			break
		}
		if !m.BaselineSet {
			m.Baseline, m.BaselineSet, m.Partial = usage, true, true
		}
		m.Native = usage
	default:
		return nil
	}
	return r.history.Store.Transaction(ctx, r.history.Writes, r.saveLedger)
}

// ObserveLogicalWork applies admitted waits without changing native settlement.
// Unchanged publication events neither move the clock nor write the ledger.
func (r *ClaudeRuntime) ObserveLogicalWork(ctx context.Context, member *control.Member, now time.Time) error {
	if member == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.identity(member.Thread); err != nil {
		return err
	}
	lifecycle := &r.ledger.Metrics.Lifecycle
	before := *lifecycle
	stop := member.Stop
	waiting := stop != nil && stop.Generation == member.Generation &&
		(member.Status == control.Working || member.Status == control.Waiting) &&
		(stop.Turn == member.Turn || stop.Turn == r.turn || r.turn == "" && member.Status == control.Waiting)
	lifecycle.Paused = member.Status == control.Paused || member.Status == control.Uncertain
	lifecycle.SetWaiting(waiting, now)
	if !waiting && lifecycle.Responding != member.HostRunning {
		lifecycle.ObserveActive(member.HostRunning, now)
	}
	if *lifecycle == before {
		return nil
	}
	return r.history.Store.Transaction(ctx, r.history.Writes, r.saveLedger)
}

func claudeReportedUsage(raw jsontext.Value) (claudeUsage, error) {
	var event struct {
		Type   string   `json:"type"`
		Cost   *float64 `json:"total_cost_usd"`
		Models map[string]struct {
			Input    *uint64 `json:"inputTokens"`
			Output   *uint64 `json:"outputTokens"`
			Read     *uint64 `json:"cacheReadInputTokens"`
			Write    *uint64 `json:"cacheCreationInputTokens"`
			Thinking *uint64 `json:"thinkingTokens"`
		} `json:"modelUsage"`
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		return claudeUsage{}, err
	}
	usage := claudeUsage{Cost: event.Cost}
	if len(event.Models) == 0 {
		if event.Type == "mekugi_usage_baseline" && event.Models != nil && event.Cost != nil && *event.Cost == 0 {
			usage.TotalsKnown = true
			return usage, nil
		}
		return usage, errors.New("Claude result omitted model usage")
	}
	var models []string
	for model, counts := range event.Models {
		if counts.Input == nil || counts.Output == nil || counts.Read == nil || counts.Write == nil {
			return usage, errors.New("Claude model usage omitted token categories")
		}
		models = append(models, model)
		for _, pair := range []struct {
			value *uint64
			add   uint64
		}{
			{&usage.InputTokens, *counts.Input}, {&usage.InputTokens, *counts.Read}, {&usage.InputTokens, *counts.Write},
			{&usage.UncachedInputTokens, *counts.Input}, {&usage.UncachedInputTokens, *counts.Write},
			{&usage.CacheWriteTokens, *counts.Write}, {&usage.OutputTokens, *counts.Output},
		} {
			sum, overflow := bits.Add64(*pair.value, pair.add, 0)
			if overflow != 0 {
				return usage, errors.New("Claude token usage overflow")
			}
			*pair.value = sum
		}
		if counts.Thinking != nil {
			sum, overflow := bits.Add64(usage.ReasoningTokens, *counts.Thinking, 0)
			if overflow != 0 || *counts.Thinking > *counts.Output {
				return usage, errors.New("invalid Claude thinking usage")
			}
			usage.ReasoningTokens = sum
		}
	}
	if usage.Cost != nil && (*usage.Cost < 0 || math.IsNaN(*usage.Cost) || math.IsInf(*usage.Cost, 0)) {
		usage.Cost = nil
	}
	slices.Sort(models)
	usage.Models, usage.TotalsKnown = strings.Join(models, ", "), true
	return usage, nil
}
