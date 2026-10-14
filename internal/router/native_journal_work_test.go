package router

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/yusing/mekugi/internal/appserver"
)

func hostJournalIOFixture(t *testing.T, mode, parent string) (*appServerUI, *appServerTestInput) {
	t.Helper()
	d, wire := resetDriverPlanFixture(t, mode, parent)
	if err := d.proxy.journals.transaction(t.Context(), d.proxy.replayStore, d.workspace, d.thread, func(j *threadJournal, _ bool) error {
		j.ResetHandledTurn, j.ResetIntent = "", nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	d.intent = nil
	u, _ := newAppServerTestUI()
	u.ctx, u.proxy, u.client, u.thread, u.reset = t.Context(), d.proxy, d.client, d.thread, d
	u.agents = newLiveActivityView()
	u.session.start(d.thread, d.workspace)
	return u, wire
}

func hostJournalIOHold(t *testing.T, u *appServerUI, gate string) func() {
	t.Helper()
	held, release, released := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(released)
		hold := func() error { close(held); <-release; return nil }
		if gate == "replay" {
			if err := u.proxy.replayStore.locked(t.Context(), hold); err != nil {
				t.Error(err)
			}
			return
		}
		unlock, err := u.proxy.journals.lockState(t.Context())
		if err != nil {
			t.Error(err)
			close(held)
			return
		}
		defer unlock()
		_ = hold()
	}()
	<-held
	return sync.OnceFunc(func() { close(release); <-released })
}

// The terminal consumes a host message, then its next key, without waiting for
// storage. The same consumer also gates the continuation on durable completion.
func TestHostJournalIOConsumingStorageResponsiveness(t *testing.T) {
	for _, gate := range []string{"state", "replay"} {
		t.Run(gate, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				u, wire := hostJournalIOFixture(t, "off", "")
				release := hostJournalIOHold(t, u, gate)
				defer release()
				result := make(chan error, 1)
				go func() {
					for _, method := range []string{"turn/started", "turn/completed"} {
						if err := u.message(appserver.Message{Method: method, Params: mustMarshalJSON(map[string]any{"threadId": u.thread, "turn": map[string]string{"id": "first-turn", "status": "completed"}})}); err != nil {
							result <- err
							return
						}
					}
					_, err := u.key('x')
					result <- err
				}()
				synctest.Wait()
				responsive := false
				select {
				case err := <-result:
					if err != nil {
						t.Error(err)
					}
					responsive = true
				default:
				}
				if wire.Len() != 0 {
					t.Error("continuation sent before durable lifecycle completion")
				}
				release()
				if !responsive {
					if err := <-result; err != nil {
						t.Fatal(err)
					}
					t.Fatal("actual message consumer blocked behind held journal storage; next input key was unreachable")
				}
				drainOrchestrateWork(t, u)
				if u.draft != "x" {
					t.Fatalf("input draft=%q", u.draft)
				}
				resetDriverRequireMethods(t, wire, "turn/start")
				// Reopen from disk, without the journal's live memory snapshot.
				reopened, err := openMekugiReplayStore(u.proxy.replayStore.directory)
				if err != nil {
					t.Fatal(err)
				}
				j, exists, err := readThreadJournal(reopened, u.reset.workspace, u.thread)
				if err != nil || !exists || j.LifecycleState != "done" || !j.WorkPaused || j.ResetIntent == nil || j.ResetIntent.Phase != "starting" {
					t.Fatalf("continuation preceded durable completion: journal=%+v exists=%v err=%v", j, exists, err)
				}
			})
		})
	}
}

func TestHostJournalIOOrderedLifecycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		proxy, _ := nativeReceiptFixture(t)
		if err := proxy.journals.bindIdentity(t.Context(), proxy.replayStore, "workspace", "thread", "", "/root", true); err != nil {
			t.Fatal(err)
		}
		u, _ := newAppServerTestUI()
		u.ctx, u.proxy, u.thread, u.agents = t.Context(), proxy, "thread", newLiveActivityView()
		u.session.start(u.thread, "workspace")
		unlock, err := proxy.journals.lockState(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		for _, wire := range []string{
			`{"method":"turn/started","params":{"threadId":"thread","turn":{"id":"turn"}}}`,
			`{"method":"thread/status/changed","params":{"threadId":"thread","status":{"type":"idle"}}}`,
			`{"method":"turn/completed","params":{"threadId":"thread","turn":{"id":"turn","status":"failed"}}}`,
			`{"method":"turn/started","params":{"threadId":"thread","turn":{"id":"next"}}}`,
			`{"method":"turn/completed","params":{"threadId":"thread","turn":{"id":"next","status":"completed"}}}`,
		} {
			appServerTestMessage(t, u, wire)
		}
		u.session.start("replacement", "another-workspace")
		u.thread = "replacement"
		unlock()
		for _, want := range []struct {
			state  string
			paused bool
		}{{"working", false}, {"working", true}, {"blocked", true}, {"working", false}, {"done", true}} {
			complete := <-u.orchestrateCompletions
			// Hold storage again while the UI applies this result and starts the
			// next worker. Read the atomic durable record before that worker writes.
			unlock, err := proxy.journals.lockState(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			u.completeOrchestrateWork(complete)
			j := nativeReceiptRecord(t, proxy)
			unlock()
			if j.LifecycleState != want.state || j.WorkPaused != want.paused {
				t.Fatalf("out-of-order lifecycle: state=%s paused=%v want=%+v", j.LifecycleState, j.WorkPaused, want)
			}
		}
		if len(u.orchestrateJobs) != 0 {
			t.Fatal("lifecycle worker retained after completion")
		}
		if _, exists, err := readThreadJournal(proxy.replayStore, "another-workspace", "thread"); err != nil || exists {
			t.Fatalf("observation followed changed UI workspace: exists=%v err=%v", exists, err)
		}
	})
}

func TestHostJournalIOCancellationAndFailure(t *testing.T) {
	for _, outcome := range []string{"interrupt", "shutdown", "failure"} {
		t.Run(outcome, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				u, wire := hostJournalIOFixture(t, "off", "")
				unlock, err := u.proxy.journals.lockState(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				if outcome == "failure" {
					u.proxy.replayStore.maxBytes = 1
				}
				u.turn = "first-turn"
				appServerTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"`+u.thread+`","turn":{"id":"first-turn","status":"completed"}}}`)
				var closed chan error
				if outcome == "interrupt" {
					if err := u.interruptTurn(); err != nil {
						t.Fatal(err)
					}
				} else if outcome == "shutdown" {
					closed = make(chan error, 1)
					go func() { closed <- u.closeOrchestrateStorage() }()
					synctest.Wait()
					select {
					case err := <-closed:
						t.Fatalf("shutdown abandoned held storage: %v", err)
					default:
					}
				}
				unlock()
				if closed != nil {
					if err := <-closed; err != nil {
						t.Fatal(err)
					}
				} else {
					drainOrchestrateWork(t, u)
				}
				if wire.Len() != 0 {
					t.Fatalf("%s dispatched continuation: %s", outcome, wire.String())
				}
				if outcome == "failure" {
					if !u.noticeAlert || !strings.Contains(u.orchestrateStorageErr.Error(), "journal lifecycle") || u.orchestrateStorageErr == nil {
						t.Fatalf("failure was not surfaced: %q, %v", u.notice, u.orchestrateStorageErr)
					}
				} else {
					j, _, err := readThreadJournal(u.proxy.replayStore, u.reset.workspace, u.thread)
					if err != nil || j.LifecycleState != "done" || j.ResetIntent != nil {
						t.Fatalf("cancellation lost completion or retained dispatch intent: %+v, %v", j, err)
					}
				}
			})
		})
	}
}

func TestHostJournalIODependentCompletionStorageResponsiveness(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		u, _ := hostJournalIOFixture(t, "off", "")
		u.session.paths = nil // Existing pre-metadata fallback isolates reset.completed.
		u.turn = "first-turn"
		unlock, err := u.proxy.journals.lockState(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		result := make(chan error, 1)
		go func() {
			result <- u.message(appserver.Message{Method: "turn/completed", Params: []byte(`{"threadId":"` + u.thread + `","turn":{"id":"first-turn","status":"completed"}}`)})
		}()
		synctest.Wait()
		responsive := false
		select {
		case err := <-result:
			if err != nil {
				t.Error(err)
			}
			responsive = true
		default:
		}
		unlock()
		if !responsive {
			if err := <-result; err != nil {
				t.Fatal(err)
			}
			t.Fatal("dependent reset.completed blocked terminal message consumer")
		}
		drainOrchestrateWork(t, u)
	})
}

func TestHostJournalIOContextUsage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		u, wire := hostJournalIOFixture(t, "auto", "/1")
		appServerTestMessage(t, u, `{"method":"thread/tokenUsage/updated","params":{"threadId":"`+u.thread+`","tokenUsage":{"last":{"totalTokens":149999},"modelContextWindow":200000}}}`)
		if got := u.proxy.contextUsage[u.thread]; got.tokens != 149999 || got.window != 200000 {
			t.Fatalf("context usage=%+v", got)
		}
		unlock, err := u.proxy.journals.lockState(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		u.turn = "first-turn"
		appServerTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"`+u.thread+`","turn":{"id":"first-turn","status":"completed"}}}`)
		appServerTestMessage(t, u, `{"method":"thread/tokenUsage/updated","params":{"threadId":"`+u.thread+`","tokenUsage":{"last":{"totalTokens":200000},"modelContextWindow":200000}}}`)
		unlock()
		drainOrchestrateWork(t, u)
		resetDriverRequireMethods(t, wire, "turn/start") // The later usage cannot change the earlier continuation decision.
		appServerTestMessage(t, u, `{"method":"item/completed","params":{"threadId":"`+u.thread+`","turnId":"first-turn","item":{"id":"compact","type":"contextCompaction"}}}`)
		if _, known := u.proxy.contextUsage[u.thread]; known {
			t.Fatal("compaction did not clear context usage synchronously")
		}
		drainOrchestrateWork(t, u)
	})
}

func TestHostJournalIOActiveSteering(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		u, wire := hostJournalIOFixture(t, "off", "")
		unlock, err := u.proxy.journals.lockState(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		appServerTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"`+u.thread+`","turn":{"id":"first-turn"}}}`)
		appServerTestKeys(t, u, "steer\r")
		resetDriverRequireMethods(t, wire, "turn/steer")
		unlock()
		drainOrchestrateWork(t, u)
	})
}

func TestHostJournalIOResetMessagesAndAcknowledgement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		u, wire := hostJournalIOFixture(t, "slice", "")
		u.journal = u.proxy.journals.attachNative(u.reset.workspace, u.thread)
		defer u.proxy.journals.detachNative(u.journal)
		if err := u.proxy.journals.restoreNative(t.Context(), u.proxy.replayStore, u.journal); err != nil {
			t.Fatal(err)
		}
		u.turn = "first-turn"
		orchestrateTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"`+u.thread+`","turn":{"id":"first-turn","status":"completed"}}}`)
		resetDriverRequireMethods(t, wire, "thread/compact/start")
		orchestrateTestMessage(t, u, fmt.Sprintf(`{"id":%s,"result":{}}`, u.reset.requestID))
		appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": u.thread, "turn": map[string]string{"id": "reset-turn"}})
		_, expected := deliverRecoveryCompaction(t, u.proxy, u.reset.workspace, u.thread, "reset-turn")
		release := hostJournalIOHold(t, u, "replay")
		defer release()
		result := make(chan error, 1)
		go func() {
			for _, m := range []appserver.Message{
				{Method: "item/completed", Params: []byte(`{"threadId":"` + u.thread + `","turnId":"reset-turn","item":{"id":"reset-item","type":"contextCompaction"}}`)},
				{Method: "turn/completed", Params: []byte(`{"threadId":"` + u.thread + `","turn":{"id":"reset-turn","status":"completed"}}`)},
			} {
				if err := u.message(m); err != nil {
					result <- err
					return
				}
			}
			_, err := u.key('x')
			result <- err
		}()
		synctest.Wait()
		responsive := false
		select {
		case err := <-result:
			if err != nil {
				t.Error(err)
			}
			responsive = true
		default:
		}
		resetDriverRequireMethods(t, wire, "thread/compact/start")
		release()
		if !responsive {
			if err := <-result; err != nil {
				t.Fatal(err)
			}
			t.Fatal("live reset notifications blocked the terminal consumer")
		}
		// The reset note is durable, but its disclosure worker has not started.
		// A second held storage interval must not move the wait into painting.
		complete := <-u.orchestrateCompletions
		if _, err := u.proxy.journals.apply(t.Context(), u.proxy.replayStore, u.reset.workspace, u.thread, "", []journalMutation{{Op: "log", Text: new("Later milestone")}}); err != nil {
			t.Fatal(err)
		}
		j, _, err := readThreadJournal(u.proxy.replayStore, u.reset.workspace, u.thread)
		if err != nil {
			t.Fatal(err)
		}
		var resetSeq uint64
		for _, event := range j.Events {
			if event.Op == "reset" {
				resetSeq = event.Seq
			}
		}
		if resetSeq == 0 {
			t.Fatal("reset message did not persist its disclosure milestone")
		}
		releaseDisclosure := hostJournalIOHold(t, u, "replay")
		defer releaseDisclosure()
		u.completeOrchestrateWork(complete)
		for _, publication := range u.journal.snapshot() {
			if publication.item.Updated >= resetSeq {
				t.Fatal("paint receipt could pass an unresolved reset disclosure")
			}
		}
		go func() { u.applyPendingJournal(); _, err := u.key('y'); result <- err }()
		synctest.Wait()
		responsive = false
		select {
		case err := <-result:
			if err != nil {
				t.Error(err)
			}
			responsive = true
		default:
		}
		releaseDisclosure()
		if !responsive {
			if err := <-result; err != nil {
				t.Fatal(err)
			}
			t.Fatal("pending reset publication moved its storage wait into the UI paint path")
		}
		drainOrchestrateWork(t, u)
		resetDriverRequireMethods(t, wire, "thread/compact/start", "turn/start")
		u.applyPendingJournal()
		resets := 0
		for _, entry := range u.view.entries {
			if entry.journalEvent != nil && entry.journalEvent.Op == "reset" {
				resets++
				if entry.native == nil || entry.native.recovery != expected {
					t.Fatal("live reset disclosure lost its exact retained recovery")
				}
			}
		}
		if resets != 1 {
			t.Fatalf("reset rows=%d", resets)
		}
		releaseAck := hostJournalIOHold(t, u, "state")
		defer releaseAck()
		ack := appserver.Message{ID: []byte(u.reset.requestID), Result: []byte(`{"turn":{"id":"continued"}}`)}
		go func() {
			err := u.message(ack)
			if err == nil {
				_, err = u.key('z')
			}
			result <- err
		}()
		synctest.Wait()
		responsive = false
		select {
		case err := <-result:
			if err != nil {
				t.Error(err)
			}
			responsive = true
		default:
		}
		releaseAck()
		if !responsive {
			if err := <-result; err != nil {
				t.Fatal(err)
			}
			t.Fatal("continuation acknowledgement cleanup blocked terminal input")
		}
		drainOrchestrateWork(t, u)
		j, _, err = readThreadJournal(u.proxy.replayStore, u.reset.workspace, u.thread)
		if err != nil || j.ResetIntent != nil {
			t.Fatalf("acknowledgement did not durably clear its intent: %+v, %v", j.ResetIntent, err)
		}
		if u.draft != "xyz" {
			t.Fatalf("input lost across reset IO: %q", u.draft)
		}
	})
}

func TestHostJournalIOResetPublicationBeforeDisclosureHold(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		u, _ := hostJournalIOFixture(t, "slice", "")
		u.journal = u.proxy.journals.attachNative(u.reset.workspace, u.thread)
		defer u.proxy.journals.detachNative(u.journal)
		if err := u.proxy.journals.restoreNative(t.Context(), u.proxy.replayStore, u.journal); err != nil {
			t.Fatal(err)
		}
		u.turn = "first-turn"
		orchestrateTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"`+u.thread+`","turn":{"id":"first-turn","status":"completed"}}}`)
		orchestrateTestMessage(t, u, fmt.Sprintf(`{"id":%s,"result":{}}`, u.reset.requestID))
		orchestrateTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"`+u.thread+`","turn":{"id":"reset-turn"}}}`)
		deliverRecoveryCompaction(t, u.proxy, u.reset.workspace, u.thread, "reset-turn")
		// Pause the UI before publication draining while the reset worker writes.
		u.proxy.activity.mu.Lock()
		result := make(chan error, 1)
		go func() {
			err := u.message(appserver.Message{Method: "item/completed", Params: []byte(`{"threadId":"` + u.thread + `","turnId":"reset-turn","item":{"id":"reset-item","type":"contextCompaction"}}`)})
			if err == nil {
				_, err = u.key('x')
			}
			result <- err
		}()
		select {
		case complete := <-u.orchestrateCompletions:
			u.orchestrateCompletions <- complete
		case <-time.After(5 * time.Second):
			u.proxy.activity.mu.Unlock()
			t.Fatal("reset worker did not reach durable publication")
		}
		release := hostJournalIOHold(t, u, "replay")
		defer release()
		u.proxy.activity.mu.Unlock()
		synctest.Wait()
		responsive := false
		select {
		case err := <-result:
			if err != nil {
				t.Error(err)
			}
			responsive = true
		default:
		}
		release()
		if !responsive {
			if err := <-result; err != nil {
				t.Fatal(err)
			}
			drainOrchestrateWork(t, u)
			t.Fatal("reset publication reached a synchronous recovery read before its disclosure hold; next input key was unreachable")
		}
		drainOrchestrateWork(t, u)
	})
}

func TestHostJournalIOCompactionActivityAfterLaterInput(t *testing.T) {
	for _, descendant := range []bool{false, true} {
		t.Run(fmt.Sprintf("descendant=%v", descendant), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				u, _ := hostJournalIOFixture(t, "off", "")
				thread := u.thread
				if descendant {
					thread = "child"
					u.session.registerThread(appServerThreadInfo{ID: thread, ParentThreadID: u.thread, AgentNickname: thread})
				}
				release := hostJournalIOHold(t, u, "state")
				defer release()
				appServerTestMessage(t, u, fmt.Sprintf(`{"method":"turn/started","params":{"threadId":%q,"turn":{"id":"turn"}}}`, thread))
				appServerTestMessage(t, u, fmt.Sprintf(`{"method":"item/completed","params":{"threadId":%q,"turnId":"turn","item":{"id":"compact-item","type":"contextCompaction"}}}`, thread))
				appServerTestMessage(t, u, fmt.Sprintf(`{"method":"item/reasoning/summaryTextDelta","params":{"threadId":%q,"turnId":"turn","itemId":"reasoning-item","delta":"Later reasoning"}}`, thread))
				visible := func() bool {
					for _, entry := range u.agents.entries {
						if entry.native != nil && entry.native.thread == thread && entry.native.item == "compact-item" && entry.Text != "" {
							return true
						}
					}
					return false
				}
				if descendant && !visible() {
					t.Error("descendant compaction progress was deferred behind lifecycle storage")
				}
				release()
				drainOrchestrateWork(t, u)
				if !visible() {
					t.Fatal("compaction completion disappeared after later reasoning activity")
				}
			})
		})
	}
}
