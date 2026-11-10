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
	hub     *Hub
	service *threadcontrol.Service
	inputs  []threadcontrol.NativeInput
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
func (h *progressDeliveryHost) Input(_ context.Context, input threadcontrol.NativeInput) (string, error) {
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
				host := &progressDeliveryHost{hub: h, service: service}
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
				if len(host.inputs) != 1 || host.inputs[0].Wake == nil || host.inputs[0].Wake.State != "yielded" || !slices.Equal(host.inputs[0].Wake.Targets, []string{"/root/worker"}) {
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
