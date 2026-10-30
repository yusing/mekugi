package sessionapi

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/diffmodel"
	changes "github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/modules/livepreview/preview"
)

func TestReviewPagePreservesLongPaths(t *testing.T) {
	path := "/project/" + strings.Repeat("long-path/", 2000) + "file"
	view := &changes.ReviewView{Revision: 1, Workspace: "/project"}
	capture := reviewChunk(1, "/root", path, path, "old\n", "new\n")
	capture.Key = strings.Repeat("\x01", 219)
	view.Publication = view.Publication.With(changes.GroupCaptures([]changes.Chunk{capture}))
	server, hub := New(), NewHub(t.Context(), nil)
	registerTestSession(hub, "thread", server, Summary{Thread: "thread", Workspace: "/project"})
	if err := server.Publish(Frame{Session: Session{Thread: "thread"}, Review: view}); err != nil {
		t.Fatal(err)
	}
	value, failure := server.page(testJSON(t, PageCursor{Kind: "review", Attachment: server.readAttachment, Revision: 1}))
	if failure != nil {
		t.Fatal(failure)
	}
	page := value.(PageResult)
	if len(page.Files) != 1 || len(testJSON(t, page)) > feedBytes {
		t.Fatal("long path metadata was not bounded")
	}
	request := httptest.NewRequest(http.MethodPost, ResourcePath+"content", bytes.NewReader(testJSON(t, RPCParams{Thread: "thread", Body: testJSON(t, page.Files[0].Content)})))
	reply := httptest.NewRecorder()
	hub.Handler().ServeHTTP(reply, request)
	var content ContentResult
	if reply.Code != 200 || json.Unmarshal(reply.Body.Bytes(), &content) != nil || content.Review == nil {
		t.Fatalf("content reference failed admission: %s", reply.Body)
	}
	file := content.Review
	if file.Path != path || file.Key != page.Files[0].Key || len(file.Chunks) != 1 {
		t.Fatal("long path content was lost")
	}
}

func reviewSubscriber(t *testing.T, server *Server) *rpcSubscription {
	t.Helper()
	sub, err := server.subscribe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.close)
	return sub
}

func reviewEvent(t *testing.T, sub *rpcSubscription, kind string) map[string]jsontext.Value {
	t.Helper()
	select {
	case event := <-sub.events:
		if event.Kind != kind {
			t.Fatalf("event kind = %q, want %q: %s", event.Kind, kind, event.Data)
		}
		var fields map[string]jsontext.Value
		if err := json.Unmarshal(event.Data, &fields); err != nil {
			t.Fatal(err)
		}
		return fields
	default:
		t.Fatal("missing publication")
		return nil
	}
}

func publishReviewFrame(t *testing.T, server *Server, frame Frame) {
	t.Helper()
	if err := server.Publish(frame); err != nil {
		t.Fatal(err)
	}
}

func reviewChunk(order uint64, caller, beforePath, afterPath, before, after string) changes.Chunk {
	return changes.Chunk{
		Key: fmt.Sprintf("capture-%d", order), CaptureOrder: order, Workspace: "/project",
		Change: fmt.Sprintf("change-%d", order), Caller: caller, Source: "apply_patch",
		Review: diffmodel.RenderReviewFile(beforePath, afterPath, before, after),
	}
}

func TestPublishReviewComposition(t *testing.T) {
	t.Parallel()
	captures := []changes.Chunk{
		reviewChunk(1, "/root", "/project/a", "/project/a", "old\n", "draft\nextra\n"),
		reviewChunk(2, "/root/worker", "/project/a", "/project/a", "draft\nextra\n", "final\n"),
		reviewChunk(3, "/root", "", "/tmp/scratch", "", "scratch\n"),
		reviewChunk(4, "/root", "/tmp/moved", "/project/moved", "old\n", "new\n"),
	}
	view := changes.ReviewView{Revision: 7, Workspace: "/project", Coverage: "complete"}
	view.Publication = view.Publication.With(changes.GroupCaptures(captures))
	server := NewLive()
	sub := reviewSubscriber(t, server)
	publishReviewFrame(t, server, Frame{Review: &view})
	first := reviewEvent(t, sub, "snapshot")
	var got savedReview
	if err := json.Unmarshal(first["review"], &got); err != nil {
		t.Fatal(err)
	}
	if got.Page == nil || got.FileCount != 2 {
		t.Fatalf("initial review must contain metadata only: %+v", got)
	}
	readFiles := func(caller string) []reviewFile {
		t.Helper()
		cursor := *got.Page
		cursor.Caller = caller
		var files []reviewFile
		for {
			value, failure := server.page(testJSON(t, cursor))
			if failure != nil {
				t.Fatal(failure)
			}
			page := value.(PageResult)
			for _, metadata := range page.Files {
				if len(metadata.Chunks) != 0 || metadata.Content == nil {
					t.Fatal("metadata page transferred complete diff")
				}
				content, failure := server.content(testJSON(t, metadata.Content))
				if failure != nil {
					t.Fatal(failure)
				}
				files = append(files, *content.(ContentResult).Review)
			}
			if page.Next == nil {
				return files
			}
			cursor = *page.Next
		}
	}
	files := readFiles("")
	if got.Revision != 7 || got.Workspace != "/project" || got.Coverage != "complete" || len(files) != 2 {
		t.Fatalf("saved review = %+v", got)
	}
	a := files[0]
	if a.Key != reviewIdentity("capture-1") || a.Path != "/project/a" || len(a.Chunks) != 1 || a.Counts != (diffmodel.Counts{Added: 1, Removed: 1}) {
		t.Fatalf("composed file = %+v", a)
	}
	if diff := a.Chunks[0].Review.Diff; !bytes.Contains([]byte(diff), []byte("-old")) || !bytes.Contains([]byte(diff), []byte("+final")) || bytes.Contains([]byte(diff), []byte("draft")) {
		t.Fatalf("uncomposed diff: %s", diff)
	}
	if !reflect.DeepEqual(a.Origins, []changes.Origin{captures[0].Origin, captures[1].Origin}) {
		t.Fatalf("origins = %+v", a.Origins)
	}
	move := files[1]
	if len(move.Chunks) != 1 || move.Chunks[0].Review.BeforePath != "/tmp/moved" || move.Chunks[0].Review.AfterPath != "/project/moved" {
		t.Fatalf("crossing move = %+v", move)
	}
	if got.Counts.Net == nil || *got.Counts.Net != (diffmodel.Counts{Added: 2, Removed: 2}) || got.Counts.Callers["/root/worker"] != (diffmodel.Counts{Added: 1, Removed: 2}) {
		t.Fatalf("counts = %+v", got.Counts)
	}
	if len(got.Callers) != 2 || got.Callers[0].Caller != "/root" || got.Callers[1].Caller != "/root/worker" {
		t.Fatalf("callers = %+v", got.Callers)
	}
	if root := readFiles(got.Callers[0].Caller); len(root) != 2 || root[0].Key != a.Key || root[1].Key != move.Key {
		t.Fatalf("main's files = %+v", root)
	}
	// As in the terminal's caller filter, a region the worker touched shows
	// its net outcome, and only the worker's change is its origin.
	worker := readFiles(got.Callers[1].Caller)
	if len(worker) != 1 || worker[0].Key != a.Key || !reflect.DeepEqual(worker[0].Chunks, a.Chunks) || worker[0].Counts != a.Counts {
		t.Fatalf("worker's files = %+v", worker)
	}
	if !reflect.DeepEqual(worker[0].Origins, []changes.Origin{captures[1].Origin}) {
		t.Fatalf("worker's origins = %+v", worker[0].Origins)
	}
	if len(view.Publication.Files) != 3 {
		t.Fatal("saved-pane filtering mutated retained evidence")
	}
	late := reviewSubscriber(t, server)
	if snapshot := reviewEvent(t, late, "snapshot"); !bytes.Equal(snapshot["review"], first["review"]) {
		t.Fatal("late subscriber lost cached review")
	}
}

func TestPublishReviewRevisionAndAttachment(t *testing.T) {
	t.Parallel()
	server := NewLive()
	sub := reviewSubscriber(t, server)
	view := changes.ReviewView{Revision: 1, Workspace: "/project", Coverage: "loading"}
	frame := Frame{Session: Session{Thread: "one"}, Review: &view}
	publishReviewFrame(t, server, frame)
	first := reviewEvent(t, sub, "snapshot")["review"]
	// A replacement with the same revision is not reprojected or encoded.
	replacement := view
	replacement.Coverage, replacement.Failure = "failed", "history unavailable"
	frame.Review = &replacement
	publishReviewFrame(t, server, frame)
	select {
	case event := <-sub.events:
		t.Fatalf("unchanged revision published: %s", event.Data)
	default:
	}
	if !bytes.Equal(server.review, first) {
		t.Fatal("unchanged revision replaced the cache")
	}
	replacement.Revision++
	publishReviewFrame(t, server, frame)
	patch := reviewEvent(t, sub, "patch")
	var got savedReview
	if err := json.Unmarshal(patch["review"], &got); err != nil {
		t.Fatal(err)
	}
	if got.Coverage != "failed" || got.Failure != "history unavailable" {
		t.Fatalf("failure state = %+v", got)
	}
	for _, reset := range []string{"attachment", "thread"} {
		if reset == "attachment" {
			frame.Attachment++
		} else {
			frame.Session.Thread = "two"
		}
		replacement.Coverage, replacement.Failure = reset, ""
		publishReviewFrame(t, server, frame)
		if err := json.Unmarshal(reviewEvent(t, sub, "snapshot")["review"], &got); err != nil {
			t.Fatal(err)
		}
		if got.Coverage != reset {
			t.Fatalf("%s retained previous attachment's revision cache", reset)
		}
	}
	frame.Review = nil
	publishReviewFrame(t, server, frame)
	if raw := reviewEvent(t, sub, "patch")["review"]; string(raw) != "null" {
		t.Fatalf("clear review = %s", raw)
	}
	frame.Review = &replacement
	publishReviewFrame(t, server, frame)
	if raw := reviewEvent(t, sub, "patch")["review"]; len(raw) == 0 {
		t.Fatal("cleared revision could not be republished")
	}
}

func TestPublishReviewCacheWork(t *testing.T) {
	// A large temporary-only publication forces ForDiff to rebuild composition
	// if rescanned. Unchanged frames must not pay that work or JSON encoding.
	var captures []changes.Chunk
	for i := range 64 {
		captures = append(captures, reviewChunk(uint64(i+1), "/root", "", fmt.Sprintf("/tmp/file-%d", i), "", "text\n"))
	}
	view := changes.ReviewView{Revision: 1, Workspace: "/project"}
	view.Publication = view.Publication.With(changes.GroupCaptures(captures))
	server := NewLive()
	frame := Frame{Review: &view}
	publishReviewFrame(t, server, frame)
	cached := testing.AllocsPerRun(20, func() { publishReviewFrame(t, server, frame) })
	revised := testing.AllocsPerRun(20, func() {
		view.Revision++
		publishReviewFrame(t, server, frame)
	})
	if revised <= cached*4 {
		t.Fatalf("review cache did not avoid projection work: cached %.0f, revised %.0f allocations", cached, revised)
	}
}

func TestPublishLivePreviewLifecycle(t *testing.T) {
	t.Parallel()
	server := NewLive()
	sub := reviewSubscriber(t, server)
	frame := Frame{Live: []preview.Preview{{ID: "edit", Caller: "/root", Input: "partial", Status: "streaming"}}}
	for i := range 3 {
		if i == 1 {
			frame.Live[0].Input = "complete input"
			frame.Live[0].Files = []diffmodel.ReviewFile{diffmodel.RenderReviewFile("a", "a", "old\n", "new\n")}
		} else if i == 2 {
			frame.Live[0].Complete, frame.Live[0].Status = true, "complete"
		}
		publishReviewFrame(t, server, frame)
		kind := "patch"
		if i == 0 {
			kind = "snapshot"
		}
		var got []preview.Preview
		if err := json.Unmarshal(reviewEvent(t, sub, kind)["live"], &got); err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].ID != "edit" || got[0].Caller != "/root" || got[0].Input != frame.Live[0].Input || got[0].Status != frame.Live[0].Status || got[0].Complete != frame.Live[0].Complete || len(got[0].Files) != len(frame.Live[0].Files) {
			t.Fatalf("live frame %d = %+v, want %+v", i, got, frame.Live)
		}
		if i > 0 && got[0].Files[0].Diff != frame.Live[0].Files[0].Diff {
			t.Fatalf("live frame %d lost its diff: %+v", i, got[0].Files)
		}
	}
	frame.Live = nil
	publishReviewFrame(t, server, frame)
	if raw := reviewEvent(t, sub, "patch")["live"]; string(raw) != "[]" {
		t.Fatalf("clear previews = %s", raw)
	}
	late := reviewSubscriber(t, server)
	if raw := reviewEvent(t, late, "snapshot")["live"]; string(raw) != "[]" {
		t.Fatalf("late subscriber retained previews: %s", raw)
	}
	replay := New()
	publishReviewFrame(t, replay, Frame{Playback: &Playback{}})
	fields := reviewEvent(t, reviewSubscriber(t, replay), "snapshot")
	if fields["live"] != nil || fields["review"] != nil {
		t.Fatalf("replay acquired live-only state: %s", fields)
	}
}
