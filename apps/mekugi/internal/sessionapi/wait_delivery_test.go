package sessionapi

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/libraries/diffrender"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	threadbinding "github.com/yusing/mekugi/modules/threadcontrol/codexbinding"
	threadcontrol "github.com/yusing/mekugi/modules/threadcontrol/control"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

type progressDeliveryRuntime struct{ threadcontrol.Runtime }

type progressDeliveryHost struct {
	threadcontrol.Native
	progressDeliveryRuntime
	hub        *Hub
	service    *threadcontrol.Service
	driver     *threadbinding.Driver
	summaries  int
	checkpoint func(threadcontrol.NativeInput) string
	inputs     []threadcontrol.NativeInput
}

func (h *progressDeliveryHost) Resume(context.Context, string) (threadcontrol.Runtime, error) {
	return h, nil
}
func (h *progressDeliveryHost) Runtime(string) (threadcontrol.Runtime, error)          { return h, nil }
func (*progressDeliveryHost) ContextIdentity(string) string                            { return "context" }
func (*progressDeliveryHost) Confirm(context.Context, threadcontrol.NativeInput) error { return nil }
func (*progressDeliveryHost) ObserveAdmitted(string, string, string, bool) bool        { return true }
func (*progressDeliveryHost) Inspect(context.Context, string) (threadcontrol.SessionDetails, error) {
	return threadcontrol.SessionDetails{}, nil
}
func (h *progressDeliveryHost) WaitProgress(thread string, seen map[string]int) (threadcontrol.WaitProgress, map[string]int) {
	return h.hub.WaitProgress(thread, h.service.WaitPublication(thread).Targets, seen)
}
func (h *progressDeliveryHost) Input(ctx context.Context, input threadcontrol.NativeInput) (string, error) {
	if input.Kind == "PROGRESS_CHECKPOINT" {
		h.summaries++
		summary := "Changed fresh behavior; validation not run."
		if h.checkpoint != nil {
			summary = h.checkpoint(input)
		}
		member, _ := h.service.Member(input.Thread)
		root, _ := h.service.Member("root")
		args, _ := json.Marshal(map[string]string{"target": "agent:main", "message": threadcontrol.ProgressReplyPrefix + root.Wait.Summaries[0].ID + "\n" + summary})
		_, err := h.driver.Call(ctx, threadcontrol.Identity{Thread: input.Thread, Session: member.Session, Turn: input.TargetTurn, Call: "checkpoint", Item: "checkpoint"}, "send_message", args)
		return input.TargetTurn, err
	}
	h.inputs = append(h.inputs, input)
	if input.Run {
		return fmt.Sprintf("continued-%d", len(h.inputs)), nil
	}
	return "", nil
}

func TestUISnapshotWaitProgressOnlyAwaitedAgent(t *testing.T) {
	for _, mode := range []threadcontrol.WaitMode{threadcontrol.WaitAny, threadcontrol.WaitAll} {
		t.Run(string(mode), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				check := func(err error) {
					t.Helper()
					if err != nil {
						t.Fatal(err)
					}
				}
				retained, err := history.NewService(threadcontrol.RecordKinds())
				check(err)
				store, err := retained.Open(t.Context(), t.TempDir())
				check(err)
				session, err := store.SelectSession(t.Context(), nil, history.Limits{Total: history.DefaultStorageBytes}, "root")
				check(err)
				defer session.Close()
				service := threadcontrol.New()
				check(service.AdmitRoot(t.Context(), session, &threadcontrol.Member{Thread: "root", Session: "root-session", Task: "/root", Turn: "turn", Generation: 1, Status: threadcontrol.Working, HostRunning: true, AudienceAdmitted: true}))
				driver, err := threadbinding.NewDriver(service)
				check(err)
				h := NewHub(t.Context(), nil)
				host := &progressDeliveryHost{hub: h, service: service, driver: driver}
				check(driver.Connect(host, host))
				root := familyRuntime(h, nil, Frame{Session: Session{Thread: "root", Workspace: "/project"}})
				caller := threadcontrol.Identity{Thread: "root", Session: "root-session", Turn: "turn", Item: "exec"}
				var worker *managedSession
				for i := range 6 {
					name := fmt.Sprintf("settled-%d", i)
					if i == 5 {
						name = "worker"
					}
					caller.Call = "spawn-" + name
					spawn, err := service.ReserveSpawn(t.Context(), caller, threadcontrol.SpawnRequest{Name: name, Message: "Work", Settings: threadcontrol.Settings{Workspace: "/project"}})
					check(err)
					for range 4 {
						effect, err := service.ClaimEffect(t.Context(), "root", spawn.ID)
						check(err)
						check(service.EffectAcknowledged(t.Context(), "root", spawn.ID, threadcontrol.Receipt{Phase: effect.Phase, Thread: name, Session: name + "-session", Turn: name + "-turn"}))
					}
					frame := Frame{Session: Session{Thread: name, Caller: "/root/" + name, Workspace: "/project"}, Agents: familyActivity(name, "testing")}
					if i != 5 {
						frame.Review = familyReview(reviewChunk(uint64(i+1), "/root/"+name, "", "/project/"+name, "", "old capture\n"))
						check(service.HostCompleted(t.Context(), name, name+"-turn", threadcontrol.Completed, threadcontrol.HostFailure{}))
						check(driver.DispatchPending(t.Context(), "root"))
						check(service.InputConsumed(t.Context(), caller, []string{threadcontrol.MessageItemID(host.inputs[len(host.inputs)-1].ID)}))
					}
					worker = familyRuntime(h, root, frame)
				}
				host.inputs = nil
				check(driver.Start(t.Context()))
				defer driver.Stop(t.Context())
				var rendered []string
				render := func(output string) {
					envelope, err := json.Marshal(map[string]any{"content": []map[string]string{{"type": "text", "text": output}}})
					check(err)
					attachment := new(sessionpresentation.Attachment)
					entry := attachment.CoordinationEntry(sessionpresentation.ActivityEntry{Agent: "/root", Native: &sessionpresentation.NativeItem{Phase: "item/completed"}}, observation.Item{Type: "mcpToolCall", Server: "threadcontrol", Tool: "wait_agent", Status: "completed", Arguments: []byte(`{}`), Result: envelope})
					painter := activityui.Painter{}
					painter.Theme = diffrender.DarkTheme
					for _, value := range sessionpresentation.ActivityContent(entry).Values() {
						rendered = append(rendered, painter.Block(activityui.Block{Value: value}, 100)...)
					}
				}
				register := func() {
					caller.Call += "-wait"
					output, err := driver.Call(t.Context(), caller, "wait_agent", []byte(fmt.Sprintf(`{"wait_mode":%q,"yield_time_ms":300000}`, mode)))
					check(err)
					result := service.WaitPublication("root")
					if len(output) != 0 || !slices.Equal(result.Targets, []string{"/root/worker"}) || result.State != "waiting" || len(service.Descendants("root")) != 6 {
						t.Fatalf("wait selected history or emitted an acknowledgement: %+v, %s", result, output)
					}
					publication, err := json.Marshal(result)
					check(err)
					render(string(publication))
					check(service.Execution(t.Context(), "root", caller.Turn, caller.Item, "", true))
					stop, err := service.ClaimLocalStop(t.Context(), "root", caller.Turn, caller.Item)
					check(err)
					if stop == nil {
						t.Fatal("missing committed stop")
					}
					check(service.HostCompleted(t.Context(), "root", caller.Turn, threadcontrol.Completed, threadcontrol.HostFailure{}))
				}
				tick := func() {
					time.Sleep(5 * time.Minute)
					driver.Tick(time.Now(), []string{"root"})
					synctest.Wait()
					if failures := driver.Failures(); len(failures) != 0 {
						t.Fatal(failures)
					}
				}
				register()
				for range 3 {
					tick()
					if len(host.inputs) != 0 || service.WaitPublication("root").Settled() {
						t.Fatalf("historical captures or an unchanged timer woke native inference: %+v", host.inputs)
					}
				}
				capture := reviewChunk(6, "/root/worker", "", "/project/fresh", "", "fresh\n")
				frame := *worker.server.frame.Load()
				frame.Review = familyReview(capture)
				worker.server.frame.Store(&frame)
				tick()
				if len(host.inputs) != 1 || host.summaries != 1 || host.inputs[0].Wake == nil || host.inputs[0].Wake.State != "yielded" || !slices.Equal(host.inputs[0].Wake.Targets, []string{"/root/worker"}) {
					t.Fatalf("native receiving result: %+v", host.inputs)
				}
				output := host.inputs[0].RetainedWaitOutput()
				if !strings.Contains(output, "fresh") || strings.Contains(output, "settled-") {
					t.Fatalf("native progress replayed historical groups: %s", output)
				}
				render(output)
				member, _ := service.Member("root")
				caller.Turn = member.Turn
				register()
				tick()
				if len(host.inputs) != 1 || service.WaitPublication("root").Settled() {
					t.Fatal("confirmed progress was repeated by a new wait")
				}
				driver.Completed("worker", "worker-turn", threadcontrol.Completed, threadcontrol.HostFailure{}, nil)
				synctest.Wait()
				if len(host.inputs) != 3 || host.inputs[1].Kind != "FINAL_ANSWER" || host.inputs[2].Wake == nil || host.inputs[2].Wake.State != "completed" {
					t.Fatalf("quiet wait lost final delivery or completion: %+v", host.inputs)
				}
				render(host.inputs[2].RetainedWaitOutput())
				rendered = append(rendered, "Following plain row")
				painted := ansi.Strip(strings.Join(rendered, "\n"))
				if !strings.Contains(painted, "Waiting for worker") || !strings.Contains(painted, "Finished waiting for worker: yielded") || strings.Contains(painted, "+4 more") || strings.Contains(painted, "settled-") {
					t.Fatalf("terminal differs from wait selection:\n%s", painted)
				}
				t.Logf("Native terminal rows after three quiet intervals, fresh progress and completion:\n%s", painted)
				uisnapshot.AssertTerminal(t, "testdata/snapshots/wait-progress-only-awaited.txt", rendered, 100)
			})
		})
	}
}

func TestWaitProgressFrozenIntervalsReachRequestAndCombinedYield(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		check := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		retained, err := history.NewService(threadcontrol.RecordKinds())
		check(err)
		store, err := retained.Open(t.Context(), t.TempDir())
		check(err)
		session, err := store.SelectSession(t.Context(), nil, history.Limits{Total: history.DefaultStorageBytes}, "root")
		check(err)
		defer session.Close()
		service := threadcontrol.New()
		check(service.AdmitRoot(t.Context(), session, &threadcontrol.Member{Thread: "root", Session: "root-session", Task: "/root", Turn: "turn", Generation: 1, Status: threadcontrol.Working, HostRunning: true, AudienceAdmitted: true}))
		caller := threadcontrol.Identity{Thread: "root", Session: "root-session", Turn: "turn", Call: "spawn", Item: "exec"}
		spawn, err := service.ReserveSpawn(t.Context(), caller, threadcontrol.SpawnRequest{Name: "worker", Message: "Work", Settings: threadcontrol.Settings{Workspace: "/project"}})
		check(err)
		for range 4 {
			effect, err := service.ClaimEffect(t.Context(), "root", spawn.ID)
			check(err)
			check(service.EffectAcknowledged(t.Context(), "root", spawn.ID, threadcontrol.Receipt{Phase: effect.Phase, Thread: "worker", Session: "worker-session", Turn: "worker-turn"}))
		}
		hub := NewHub(t.Context(), nil)
		root := familyRuntime(hub, nil, Frame{Session: Session{Thread: "root", Workspace: "/project"}})
		initial := reviewChunk(6, "/root/worker", "", "/project/value", "", "first interval\n")
		initial.Change = "amber1"
		later := reviewChunk(7, "/root/worker", "/project/value", "/project/value", "first interval\n", "later interval\n")
		later.Change = initial.Change
		extended := reviewChunk(6, "/root/worker", "", "/project/z-extra", "", "later published chunk\n")
		extended.Change, extended.Key = initial.Change, "extended-record"
		selections := []string{"amber1 record=6 chunks=1..1", "amber1 record=6 chunks=2..2; amber1 record=7 chunks=1..1"}
		worker := familyRuntime(hub, root, Frame{Session: Session{Thread: "worker", Caller: "/root/worker", Workspace: "/project"}, Review: familyReview(initial)})
		driver, err := threadbinding.NewDriver(service)
		check(err)
		host := &progressDeliveryHost{hub: hub, service: service, driver: driver}
		host.checkpoint = func(input threadcontrol.NativeInput) string {
			expected, excluded := selections[0], "record=7"
			if host.summaries == 2 {
				expected, excluded = selections[1], selections[0]
			}
			if !strings.Contains(input.Message, expected) || strings.Contains(input.Message, excluded) {
				t.Errorf("checkpoint selection cannot distinguish same-ID intervals: %s", input.Message)
			}
			if host.summaries == 1 {
				// The same retained ID gains another immutable writer interval while
				// the developer has the frozen request but has not replied yet.
				frame := *worker.server.frame.Load()
				frame.Review = familyReview(initial, extended, later)
				frame.Review.Revision++
				worker.server.frame.Store(&frame)
			}
			return "Only " + expected + "; missing validation."
		}
		check(driver.Connect(host, host))
		check(driver.Start(t.Context()))
		defer driver.Stop(t.Context())
		for batch := range 2 {
			if batch != 0 {
				member, _ := service.Member("root")
				caller.Turn = member.Turn
			}
			caller.Call = fmt.Sprintf("wait-%d", batch)
			_, err := driver.Call(t.Context(), caller, "wait_agent", []byte(`{"yield_time_ms":300000}`))
			check(err)
			check(service.Execution(t.Context(), "root", caller.Turn, caller.Item, "", true))
			_, err = service.ClaimLocalStop(t.Context(), "root", caller.Turn, caller.Item)
			check(err)
			check(service.HostCompleted(t.Context(), "root", caller.Turn, threadcontrol.Completed, threadcontrol.HostFailure{}))
			time.Sleep(5 * time.Minute)
			driver.Tick(time.Now(), []string{"root"})
			synctest.Wait()
			if len(host.inputs) != batch+1 || host.inputs[batch].Wake == nil || host.inputs[batch].Wake.Progress == nil {
				t.Fatalf("combined interval yield missing: inputs=%d failures=%v", len(host.inputs), driver.Failures())
			}
			entry := host.inputs[batch].Wake.Progress.Agents[0]
			expected, excluded := selections[batch], selections[1-batch]
			if !strings.Contains(entry.Changes, expected) || strings.Contains(entry.Changes, excluded) || entry.Summary != "Only "+expected+"; missing validation." {
				t.Fatalf("request/reply/combined output crossed the frozen boundary: %+v", entry)
			}
		}
		if again, _ := hub.WaitProgress("root", []string{"/root/worker"}, service.YieldCursor("root", "context")); len(again.Agents) != 0 {
			t.Fatal("confirmed interval deliveries repeated history")
		}
	})
}
