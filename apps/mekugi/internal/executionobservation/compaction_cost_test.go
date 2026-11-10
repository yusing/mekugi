package executionobservation

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"strings"
	"testing"
	"time"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/contracts/inference"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/modules/agentguidance/compact"
	"github.com/yusing/mekugi/modules/conversation/composer"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
	"github.com/yusing/mekugi/modules/usage/accounting"
)

func TestSelfCompactionCostAdmission(t *testing.T) {
	for _, mode := range []string{"compact", "wait", "reload", "yielded", "superseded", "optional-skill", "skill-ref", "cheap", "denied", "denied-wait", "canceled", "source-error", "unknown", "unknown-cold", "context-40", "context-above-40", "context-above-40-wait", "context-unknown", "context-error", "context-no-inspector", "context-zero-window"} {
		t.Run(mode, func(t *testing.T) {
			bare, service, caller := threadWaitFixture(t)
			p := newManagedMekugiProxy(t)
			attachTestReplayStore(t, p)
			session, err := p.Retained.Store.SelectSession(t.Context(), nil, history.Limits{Total: history.DefaultStorageBytes}, "root")
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			p.ThreadControl, p.Usage = bare.ThreadControl, accounting.New()
			bypass := strings.HasPrefix(mode, "context-") && mode != "context-40"
			p.Inspect = func(_ context.Context, thread string) (control.SessionDetails, error) {
				if thread != caller.Thread {
					t.Fatalf("inspected wrong caller: %q", thread)
				}
				tokens, window := uint64(39999), uint64(100000)
				switch mode {
				case "context-40":
					tokens = 40000
				case "context-above-40", "context-above-40-wait":
					tokens = 40001
				case "context-unknown":
					return control.SessionDetails{}, nil
				case "context-error":
					return control.SessionDetails{}, errors.New("inspection unavailable")
				case "context-zero-window":
					window = 0
				}
				return control.SessionDetails{SessionMetrics: control.SessionMetrics{ContextTokens: &tokens, ContextWindow: &window}}, nil
			}
			if mode == "context-no-inspector" {
				p.Inspect = nil
			}
			p.Audience.Observe(caller.Thread, "", "/root", false)
			p.Audience.Attach(caller.Thread)
			var notice string
			p.Notice = func(_, _, _, message string) { notice = message }
			counts := inference.Usage{InputTokens: 1000}
			if mode == "optional-skill" {
				counts.InputTokens = 50000
			}
			tier := "default"
			if mode == "unknown-cold" {
				tier = "priority"
			}
			if mode == "cheap" || mode == "source-error" {
				counts.UncachedInputTokens = 1000
				counts.InputTokens = 100_000
			}
			if mode != "unknown" {
				p.Usage.Observation("root", "root", "gpt-5.4", tier).Observe(counts)
			}
			selection := ""
			if mode == "reload" || mode == "yielded" || mode == "superseded" || strings.HasPrefix(mode, "denied") || mode == "canceled" || mode == "source-error" || bypass {
				selection = `{"sources":["@selected.txt"]}`
			}
			if mode == "optional-skill" || mode == "skill-ref" {
				p.CompactionAttachments = func(context.Context, string) ([]composer.ContextAttachment, error) {
					return []composer.ContextAttachment{{Skills: []composer.Token{{Name: "example/references/check.md"}}}}, nil
				}
			}
			if mode == "skill-ref" {
				selection = `{"sources":["$example/references/check.md"]}`
			}
			standalone := mode != "wait" && mode != "denied-wait" && mode != "context-above-40-wait"
			if _, err := service.RegisterCompaction(t.Context(), caller, "", nil, nil, time.Now(), standalone, selection); err != nil {
				t.Fatal(err)
			}
			items := []any{
				map[string]any{"type": "message", "role": "developer", "content": strings.Repeat("required policy ", 400)},
				map[string]any{"type": "custom_tool_call", "id": caller.Item, "call_id": caller.Call, "name": "exec", "input": "checkpoint"},
				map[string]any{"type": "custom_tool_call_output", "call_id": caller.Call, "output": "Script completed\nWall time 0.1 seconds\nOutput:\n"},
			}
			if mode == "unknown-cold" {
				items[0] = map[string]any{"type": "message", "role": "developer", "content": strings.Repeat("policy ", 280000)}
			}
			prepare := func() *mekugiResponseTransform {
				t.Helper()
				request, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{"model": "gpt-5.4", "service_tier": tier, "instructions": testBaseInstructions, "input": items, "tools": testExecResponsesTools()}))
				if err != nil {
					t.Fatal(err)
				}
				check := p.prepareCompactionCost(t.Context(), &request, caller.Thread, codexresponses.TurnMetadata{ThreadID: caller.Thread, TurnID: caller.Turn, RequestKind: "turn"})
				var calls map[string]bool
				if check != nil {
					calls = check.calls
				}
				item, err := p.observeThreadWait(t.Context(), caller.Thread, caller.Turn, "exec", &request, nil, calls)
				if err != nil {
					t.Fatal(err)
				}
				if check == nil && item != "" {
					check = p.prepareCompactionCost(t.Context(), &request, caller.Thread, codexresponses.TurnMetadata{ThreadID: caller.Thread, TurnID: caller.Turn, RequestKind: "turn"})
				}
				prepared := &mekugiResponseTransform{owners: p, ctx: t.Context(), sessionID: caller.Session, shellThreadID: caller.Thread, shellTurnID: caller.Turn, compactionCost: check, localWaitItem: item}
				if err := prepared.checkCompactionCost(&request); err != nil {
					t.Fatal(err)
				}
				if _, events := p.Audience.Take(caller.Thread); len(events) != 0 {
					t.Fatalf("cost preflight published attachments without a reset: %+v", events)
				}
				return prepared
			}
			prepared := prepare()
			if bypass && (prepared.compactionCost != nil || len(prepared.CompactReload()) != 0) {
				t.Fatal("bypassed cost gate acquired preflight reload")
			}
			if mode == "optional-skill" && (len(prepared.CompactReload()) != 0 || !strings.Contains(string(mustTestJSON(t, prepared.compactionCost.reload)), "Optional attachment recall: skills: mread ")) {
				t.Fatal("optional skill body was acquired/charged instead of its recall hint")
			}
			if selection != "" && !bypass {
				reader := prepared.CompactReload()
				if len(reader) == 0 || prepared.LocalWaitItem() != "" {
					t.Fatal("preflight did not hold local stop")
				}
				var call map[string]jsontext.Value
				if err := json.Unmarshal(reader, &call); err != nil {
					t.Fatal(err)
				}
				if !codexresponses.MatchesCompactReadProgram(responses.StringField(call, "input"), compact.CaptureCommand(selection)) {
					t.Fatal("preflight changed source admission")
				}
				if mode == "yielded" {
					items = append(items, call, map[string]any{"type": "custom_tool_call_output", "call_id": responses.StringField(call, "call_id"), "output": "Script running with cell ID slow\nWall time 10 seconds\nOutput:\n"})
					prepared.Close()
					prepared = prepare()
					call = nil
					if prepared.LocalWaitItem() != "" || json.Unmarshal(prepared.CompactReload(), &call) != nil || responses.StringField(call, "name") != "wait" {
						t.Fatal("yielded preflight did not retain native continuation")
					}
				}
				output := "Script completed\nWall time 0.1 seconds\nOutput:\nCONTENT NOT ATTACHED: command rejected by user"
				if mode == "canceled" {
					output = "Script terminated\nWall time 0.1 seconds\nOutput:\n"
				}
				if !strings.HasPrefix(mode, "denied") && mode != "canceled" {
					source := compact.CapturedSource{Command: "mcat selected.txt", Stdout: strings.Repeat("nested attachment ", 1000)}
					if mode == "source-error" {
						source.Stdout, source.Stderr, source.ExitCode = "", "source does not exist", 1
					}
					output = compactTestSnapshot(t, p, compact.Snapshot{Selection: selection, Sources: []compact.CapturedSource{source}})
				}
				items = append(items, call, map[string]any{"type": responses.StringField(call, "type") + "_output", "call_id": responses.StringField(call, "call_id"), "output": output})
				if mode == "superseded" {
					items = append(items, map[string]any{"type": "message", "role": "user", "content": "new work"})
				}
				prepared.Close()
				prepared = prepare()
			}
			defer prepared.Close()
			rejected := !bypass && mode != "cheap" && mode != "optional-skill" && mode != "source-error" && mode != "unknown" && mode != "unknown-cold"
			member, _ := service.Member(caller.Thread)
			if mode == "superseded" {
				if member.Wait.State != "canceled" || prepared.LocalWaitItem() != "" {
					t.Fatal("preflight blocked new input")
				}
				return
			}
			if (member.Wait.Compaction.State == "skipped") != rejected || prepared.LocalWaitItem() != caller.Item {
				t.Fatalf("gate state: %+v local=%q", member.Wait, prepared.LocalWaitItem())
			}
			if !rejected {
				if strings.HasPrefix(mode, "unknown") && !strings.Contains(notice, "unavailable") {
					t.Fatal("unknown cost was silent")
				}
				return
			}
			readFailed := strings.HasPrefix(mode, "denied") || mode == "canceled"
			if readFailed && !strings.Contains(member.Wait.Compaction.Error, "not repeated") {
				t.Fatal("preflight failure not reported")
			}
			if !readFailed && !strings.Contains(member.Wait.Compaction.Error, "assumes no cache hits") {
				t.Fatal("estimate assumptions missing")
			}
			if !strings.Contains(member.Wait.Compaction.Error, "Context was not reset; reload sources were not attached.") {
				t.Fatal("rejection did not explain the unchanged context and reload")
			}
			if _, err := service.ClaimLocalStop(t.Context(), caller.Thread, caller.Turn, caller.Item); err != nil {
				t.Fatal(err)
			}
			if err := service.HostCompleted(t.Context(), caller.Thread, caller.Turn, control.Completed, control.HostFailure{}); err != nil {
				t.Fatal(err)
			}
			if effect, err := service.ClaimEffect(t.Context(), caller.Thread, caller.Call+":compact"); err != nil || effect != nil {
				t.Fatalf("rejected reset dispatched: %+v %v", effect, err)
			}
			wake, err := service.PrepareWake(t.Context(), caller.Thread, time.Now())
			if err != nil || (wake == nil) != !standalone {
				t.Fatalf("wait settled too early: %+v %v", wake, err)
			}
			if !standalone {
				if err := service.AnswerCompleted(t.Context(), "child", "child-turn", "answer", "ready"); err != nil {
					t.Fatal(err)
				}
				if err := service.HostCompleted(t.Context(), "child", "child-turn", control.Completed, control.HostFailure{}); err != nil {
					t.Fatal(err)
				}
				injection, err := service.ClaimEffect(t.Context(), "child", "child:1:child-turn:final")
				if err != nil || injection == nil {
					t.Fatalf("missing outcome delivery: %+v %v", injection, err)
				}
				if err := service.EffectAcknowledged(t.Context(), "child", injection.ID, control.Receipt{}); err != nil {
					t.Fatal(err)
				}
				wake, err = service.PrepareWake(t.Context(), caller.Thread, time.Now())
				if err != nil || wake == nil {
					t.Fatalf("wait lost after rejection: %+v %v", wake, err)
				}
			}
			claimed, err := service.ClaimEffect(t.Context(), caller.Thread, wake.ID)
			if err != nil || claimed == nil || claimed.Wake == nil || claimed.Wake.Compaction.Error == "" {
				t.Fatalf("rejection missing from eventual output: %+v %v", claimed, err)
			}
		})
	}
}
