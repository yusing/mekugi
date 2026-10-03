package router

import (
	"encoding/json/jsontext"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/appserver"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func autoResumeFixture(t *testing.T) (*journalResetDriver, *appServerTestInput) {
	t.Helper()
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	thread := transform.shellThreadID
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, thread, "", []journalMutation{{Op: "plan", Tasks: []jsontext.Value{jsontext.Value(`{"title":"Finish implementation","state":"working"}`)}}}); err != nil {
		t.Fatal(err)
	}
	if err := proxy.journals.beginJournalTurn(t.Context(), proxy.replayStore, workspace, thread, "answer"); err != nil {
		t.Fatal(err)
	}
	_, wire := newAppServerTestUI()
	proxy.journalCompaction = "slice"
	return &journalResetDriver{ctx: t.Context(), proxy: proxy, client: &appserver.Client{Input: wire}, workspace: workspace, thread: thread}, wire
}

func TestJournalAutoResumeFollowupUntilComplete(t *testing.T) {
	t.Parallel()
	d, wire := autoResumeFixture(t)
	for _, turn := range []string{"answer", "continued"} {
		if err := d.completed(turn); err != nil {
			t.Fatal(err)
		}
		if d.intent == nil || !d.intent.Resume || d.intent.Path != "/1" {
			t.Fatalf("not resuming unfinished work: %+v", d.intent)
		}
		if err := d.tick(time.Now().Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		resetDriverRequireMethods(t, wire, "turn/start")
		wire.Reset()
		resetDriverEvent(t, d, "turn/started", `{"threadId":"`+d.thread+`","turn":{"id":"continued"}}`)
		resetDriverReply(t, d, `{"turn":{"id":"continued"}}`)
		if err := d.proxy.journals.beginJournalTurn(t.Context(), d.proxy.replayStore, d.workspace, d.thread, "continued"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.proxy.journals.apply(t.Context(), d.proxy.replayStore, d.workspace, d.thread, "", []journalMutation{{Op: "set", P: "/1", State: new("done")}}); err != nil {
		t.Fatal(err)
	}
	if err := d.proxy.journals.beginJournalTurn(t.Context(), d.proxy.replayStore, d.workspace, d.thread, "finished"); err != nil {
		t.Fatal(err)
	}
	if err := d.completed("finished"); err != nil {
		t.Fatal(err)
	}
	if d.active() {
		t.Fatal("completed journal continued")
	}
	resetDriverRequireMethods(t, wire)
}

func TestJournalAutoResumeAdmission(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, task, want string }{
		{"pending", `{"title":"Task"}`, "/1"},
		{"working", `{"title":"Task","state":"working"}`, "/1"},
		{"done", `{"title":"Task","state":"done"}`, ""},
		{"dropped", `{"title":"Task","state":"dropped","reason":"cancelled"}`, ""},
		{"blocked", `{"title":"Task","state":"blocked","reason":"needs input"}`, ""},
		{"nested", `{"title":"Task","state":"working","tasks":[{"title":"Child"}]}`, "/1/1"},
		{"blocked child", `{"title":"Task","state":"working","tasks":[{"title":"Child","state":"blocked","reason":"needs input"}]}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, _ := autoResumeFixture(t)
			if _, err := d.proxy.journals.apply(t.Context(), d.proxy.replayStore, d.workspace, d.thread, "", []journalMutation{{Op: "plan", Tasks: []jsontext.Value{jsontext.Value(strings.Replace(tc.task, `{"title":`, `{"p":"/1","title":`, 1))}}}); err != nil {
				t.Fatal(err)
			}
			if err := d.completed("answer"); err != nil {
				t.Fatal(err)
			}
			if tc.want == "" {
				if d.active() {
					t.Fatal("non-runnable journal continued")
				}
				return
			}
			if d.intent == nil || d.intent.Path != tc.want || !d.intent.Resume {
				t.Fatalf("intent=%+v want=%s", d.intent, tc.want)
			}
		})
	}
}

func TestJournalAutoResumeRevalidatesAndIsolatesRestart(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"pending", "done", "blocked", "new-turn", "cancelled", "dispatched", "other-thread", "other-workspace"} {
		t.Run(scenario, func(t *testing.T) {
			d, wire := autoResumeFixture(t)
			if err := d.completed("answer"); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "done", "blocked":
				if _, err := d.proxy.journals.apply(t.Context(), d.proxy.replayStore, d.workspace, d.thread, "", []journalMutation{{Op: "set", P: "/1", State: new(scenario), Reason: new("waiting")}}); err != nil {
					t.Fatal(err)
				}
			case "new-turn":
				if err := d.proxy.journals.beginJournalTurn(t.Context(), d.proxy.replayStore, d.workspace, d.thread, "new"); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				if err := d.cancel(); err != nil {
					t.Fatal(err)
				}
			case "dispatched":
				if err := d.tick(time.Now().Add(time.Second)); err != nil {
					t.Fatal(err)
				}
			}
			reopened, err := openMekugiReplayStore(d.proxy.replayStore.directory)
			if err != nil {
				t.Fatal(err)
			}
			d.proxy.replayStore, d.proxy.journals = reopened, newJournalStore()
			next := &journalResetDriver{ctx: t.Context(), proxy: d.proxy, client: d.client, workspace: d.workspace, thread: d.thread}
			if scenario == "other-thread" {
				next.thread = "other"
			}
			if scenario == "other-workspace" {
				next.workspace = t.TempDir()
			}
			wire.Reset()
			if err := next.restore(); err != nil {
				t.Fatal(err)
			}
			if next.active() != (scenario == "pending") {
				t.Fatalf("active=%v", next.active())
			}
			resetDriverRequireMethods(t, wire)
			if scenario == "pending" {
				if err := next.tick(time.Now().Add(time.Second)); err != nil {
					t.Fatal(err)
				}
				resetDriverRequireMethods(t, wire, "turn/start")
			}
		})
	}
}

func TestNativeJournalAutoResumeStopsForInputAndCancellation(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"natural", "interrupt-race", "question", "queued", "failed", "interrupted", "foreign", "stale"} {
		t.Run(scenario, func(t *testing.T) {
			d, wire := autoResumeFixture(t)
			u, _ := newAppServerTestUI()
			u.ctx, u.proxy, u.client, u.thread, u.turn, u.reset = t.Context(), d.proxy, d.client, d.thread, "answer", d
			status, thread, turn := "completed", d.thread, "answer"
			switch scenario {
			case "interrupt-race":
				if err := u.interruptTurn(); err != nil {
					t.Fatal(err)
				}
				wire.Reset()
			case "question":
				u.questions.calls = []*nativeQuestionCall{{thread: d.thread, turn: "answer", questions: []nativeQuestion{{Title: "Need a decision"}}}}
			case "queued":
				u.queued = []composerDraft{{text: "New direction"}}
			case "failed", "interrupted":
				status = scenario
			case "foreign":
				thread = "foreign"
			case "stale":
				turn = "stale"
			}
			appServerTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"`+thread+`","turn":{"id":"`+turn+`","status":"`+status+`"}}}`)
			if d.active() != (scenario == "natural") {
				t.Fatalf("active=%v", d.active())
			}
			if scenario != "queued" {
				resetDriverRequireMethods(t, wire)
			}
			if scenario == "interrupt-race" {
				if err := d.completed("answer"); err != nil {
					t.Fatal(err)
				}
				if d.active() {
					t.Fatal("successful host race revived cancelled work")
				}
			}
		})
	}
}

func TestHeadlessJournalAutoResumeRequiresQuestionInput(t *testing.T) {
	t.Parallel()
	d, _ := autoResumeFixture(t)
	h := &headlessAppServer{ctx: t.Context(), client: d.client, proxy: d.proxy, reset: d, thread: d.thread, output: jsontext.NewEncoder(io.Discard)}
	err := headlessTestMessage(t, h, "item/completed", `{"threadId":"`+d.thread+`","turnId":"answer","item":{"type":"agentMessage","delivery":"async","questions":[{"title":"Which scope?"}]}}`)
	if err == nil || d.active() {
		t.Fatalf("pending question: %v", err)
	}
}

func TestUISnapshotJournalAutoResume(t *testing.T) {
	t.Parallel()
	d, _ := autoResumeFixture(t)
	if err := d.completed("answer"); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0)
	d.deadline = now.Add(3 * time.Second)
	uisnapshot.Assert(t, "testdata/snapshots/journal-auto-resume.txt", d.label(now)+"\n")
}

func TestJournalAutoResumeOwnership(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"child", "unknown", "conflicted", "delegated", "delegated-parent", "fork"} {
		t.Run(scenario, func(t *testing.T) {
			d, wire := autoResumeFixture(t)
			if err := d.proxy.journals.transaction(t.Context(), d.proxy.replayStore, d.workspace, d.thread, func(j *threadJournal, _ bool) error {
				switch scenario {
				case "child":
					j.Parent = "parent"
				case "unknown":
					j.IdentityKnown = false
				case "conflicted":
					j.IdentityConflicted = true
				case "delegated":
					j.Items[0].Agent = "/root/child"
				case "delegated-parent":
					j.Items[0].Agent = "/root/child"
					_, err := j.applyTree(journalMutation{Op: "add", Under: "/1", Kind: "task", Title: new("Child work"), State: new("working")})
					return err
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if scenario == "fork" {
				if err := d.completed("answer"); err != nil {
					t.Fatal(err)
				}
				if err := d.proxy.journals.initialize(t.Context(), d.proxy.replayStore, d.workspace, "fork", "/root", d.thread); err != nil {
					t.Fatal(err)
				}
				d = &journalResetDriver{ctx: t.Context(), proxy: d.proxy, client: d.client, workspace: d.workspace, thread: "fork"}
				if err := d.restore(); err != nil {
					t.Fatal(err)
				}
			}
			if err := d.completed("answer"); err != nil {
				t.Fatal(err)
			}
			if d.active() {
				t.Fatal("borrowed another owner's continuation")
			}
			resetDriverRequireMethods(t, wire)
		})
	}
}

func TestJournalAutoResumeRevalidatesBeforeDispatch(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"done", "blocked", "new-turn", "delegated"} {
		t.Run(scenario, func(t *testing.T) {
			d, wire := autoResumeFixture(t)
			if err := d.completed("answer"); err != nil {
				t.Fatal(err)
			}
			if err := d.proxy.journals.transaction(t.Context(), d.proxy.replayStore, d.workspace, d.thread, func(j *threadJournal, _ bool) error {
				switch scenario {
				case "new-turn":
					j.TurnID = "new"
				case "delegated":
					j.Items[0].Agent = "/root/child"
				default:
					j.Items[0].State = scenario
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := d.tick(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			if d.active() {
				t.Fatal("stale work dispatched")
			}
			resetDriverRequireMethods(t, wire)
		})
	}
}

// An interrupt is a durable user decision, not just suppression of one host event.
func TestJournalAutoResumeUserStopSurvivesLaterQuestions(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"status-answer", "note", "same-state-status-edit", "same-turn-reactivation", "restart", "fork", "unrelated-work", "reactivate", "escape", "native-interrupted", "headless-interrupted", "atomic-rejection"} {
		t.Run(scenario, func(t *testing.T) {
			d, wire := autoResumeFixture(t)
			if scenario == "escape" {
				if err := d.completed("answer"); err != nil {
					t.Fatal(err)
				}
				u, _ := newAppServerTestUI()
				u.ctx, u.proxy, u.client, u.thread, u.reset = t.Context(), d.proxy, d.client, d.thread, d
				u.ensureShell()
				if err := u.shell.send("\x1b"); err != nil {
					t.Fatal(err)
				}
			} else if scenario == "native-interrupted" {
				u, _ := newAppServerTestUI()
				u.ctx, u.proxy, u.client, u.thread, u.turn, u.reset = t.Context(), d.proxy, d.client, d.thread, "answer", d
				appServerTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"`+d.thread+`","turn":{"id":"answer","status":"interrupted"}}}`)
			} else if scenario == "headless-interrupted" {
				h := &headlessAppServer{ctx: t.Context(), proxy: d.proxy, client: d.client, reset: d, thread: d.thread, turn: "answer", output: jsontext.NewEncoder(io.Discard)}
				if err := headlessTestMessage(t, h, "turn/completed", `{"threadId":"`+d.thread+`","turn":{"id":"answer","status":"interrupted"}}`); err == nil {
					t.Fatal("expected interrupted headless error")
				}
			} else if err := d.stop("answer"); err != nil {
				t.Fatal(err)
			}
			apply := func(mutations ...journalMutation) error {
				_, err := d.proxy.journals.apply(t.Context(), d.proxy.replayStore, d.workspace, d.thread, "", mutations)
				return err
			}
			if scenario == "same-turn-reactivation" {
				if err := apply(journalMutation{Op: "set", P: "/1", State: new("working")}); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "restart" {
				reopened, err := openMekugiReplayStore(d.proxy.replayStore.directory)
				if err != nil {
					t.Fatal(err)
				}
				d.proxy.replayStore, d.proxy.journals = reopened, newJournalStore()
			}
			if scenario == "fork" {
				if err := d.proxy.journals.initialize(t.Context(), d.proxy.replayStore, d.workspace, "fork", "/root", d.thread); err != nil {
					t.Fatal(err)
				}
				d.thread = "fork"
				if err := d.proxy.journals.bindIdentity(t.Context(), d.proxy.replayStore, d.workspace, d.thread, "", "/root", true); err != nil {
					t.Fatal(err)
				}
			}
			if err := d.proxy.journals.beginJournalTurn(t.Context(), d.proxy.replayStore, d.workspace, d.thread, "status-question"); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "note":
				if err := apply(journalMutation{Op: "log", Text: new("Work remains stopped")}); err != nil {
					t.Fatal(err)
				}
			case "same-state-status-edit":
				if err := apply(journalMutation{Op: "set", P: "/1", Body: new("Status only")}); err != nil {
					t.Fatal(err)
				}
			case "unrelated-work":
				if err := apply(journalMutation{Op: "add", Kind: "task", Title: new("Other task"), State: new("working")}); err != nil {
					t.Fatal(err)
				}
			case "reactivate":
				if err := apply(journalMutation{Op: "set", P: "/1", State: new("working")}); err != nil {
					t.Fatal(err)
				}
			case "atomic-rejection":
				if err := apply(journalMutation{Op: "set", P: "/1", State: new("working")}, journalMutation{Op: "set", P: "/missing", State: new("done")}); err == nil {
					t.Fatal("expected batch rejection")
				}
			}
			d = &journalResetDriver{ctx: t.Context(), proxy: d.proxy, client: d.client, workspace: d.workspace, thread: d.thread}
			if err := d.restore(); err != nil {
				t.Fatal(err)
			}
			if err := d.completed("status-question"); err != nil {
				t.Fatal(err)
			}
			want := ""
			if scenario == "reactivate" {
				want = "/1"
			}
			if scenario == "unrelated-work" {
				want = "/2"
			}
			if want == "" {
				if d.active() {
					t.Fatalf("stopped work revived: %+v", d.intent)
				}
			} else if d.intent == nil || d.intent.Path != want {
				t.Fatalf("intent=%+v want=%s", d.intent, want)
			}
			resetDriverRequireMethods(t, wire)
		})
	}
}
