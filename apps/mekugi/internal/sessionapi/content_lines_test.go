package sessionapi

import (
	"bytes"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/diffmodel"
	changes "github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/modules/livepreview/preview"
)

func TestContentByteOffsetsPreserveLines(t *testing.T) {
	for _, source := range []string{"", "\n", "one\n\ntwo\n\nthree\n", "日本語\nlast\n"} {
		offset := uint64(0)
		for offset < uint64(len(source)) {
			page, err := linePage(source, offset)
			if err != nil || !slices.Equal(page.Lines, strings.Split(strings.TrimSuffix(source[offset:], "\n"), "\n")) || page.Next != nil {
				t.Fatalf("source=%q offset=%d page=%+v err=%v", source, offset, page, err)
			}
			offset += uint64(strings.IndexByte(source[offset:], '\n') + 1)
		}
		if page, err := linePage(source, offset); err != nil || len(page.Lines) != 0 || page.Next != nil {
			t.Fatal("end offset changed")
		}
		if _, err := linePage(source, offset+1); err == nil {
			t.Fatal("invalid offset admitted")
		}
	}
	if _, err := linePage("line\n", 1); err == nil {
		t.Fatal("offset inside a line admitted")
	}
	line := strings.Repeat("x", 128<<10)
	source := line + "\n" + line + "\n"
	first, err := linePage(source, 0)
	if err != nil || len(first.Lines) != 1 || first.Next == nil || first.Lines[0] != line {
		t.Fatalf("large row: %+v %v", first, err)
	}
	second, err := linePage(source, *first.Next)
	if err != nil || len(second.Lines) != 1 || second.Next != nil || second.Lines[0] != line {
		t.Fatal("large row continuation lost bytes")
	}
}

func TestContentContinuationCostAndIndependentFrontends(t *testing.T) {
	f := newFrontendFixture(t)
	f.open(t, 401)
	f.open(t, 402)
	const rows = 10001
	var input strings.Builder
	fmt.Fprintf(&input, "--- a.go\n+++ a.go\n@@ -0,0 +%d @@\n", rows)
	payload := strings.Repeat("a", 1024)
	for i := range rows {
		fmt.Fprintf(&input, "+%08d %s\n", i, payload)
	}
	text := input.String()
	if len(text) <= 7<<20 {
		t.Fatal("fixture does not exceed panel response budget")
	}
	file := changes.File{Path: "a.go", Chunks: []changes.Chunk{{Origin: changes.Origin{Caller: "/root"}, Review: diffmodel.RenderReviewFile("a.go", "a.go", "", "new\n")}}}
	file.Chunks[0].Review.Diff = text
	review := &changes.ReviewView{Publication: changes.Publication{Files: []changes.File{file}, Unfiltered: map[string]changes.File{file.Key(): file}}, Revision: 1, Workspace: "/workspace", Coverage: "complete"}
	if err := f.api.Publish(Frame{Session: Session{Thread: "thread", Workspace: "/workspace"}, Attachment: 1, Review: review}); err != nil {
		t.Fatal(err)
	}
	f.api.mu.Lock()
	ref := ContentReference{Attachment: f.api.readAttachment, Kind: "review", Revision: 1, Key: reviewIdentity(file.Key())}
	f.api.mu.Unlock()
	f.hub.mu.Lock()
	frontends := map[int]*rpcConnection{}
	for owner := range f.hub.connections {
		if owner.frontend.pid != 0 {
			frontends[owner.frontend.pid] = owner
		}
	}
	f.hub.mu.Unlock()
	rpc, err := OpenRPC(t.Context(), f.server.Client(), f.server.URL+NativeRPCPath, "")
	if err != nil {
		t.Fatal(err)
	}
	defer rpc.Close()
	read := func(pid int, offset uint64) Lines {
		t.Helper()
		body, _ := json.Marshal(NativeFrontendRequest{PID: pid, Instance: frontends[pid].frontend.instance, Content: &ref, Offset: &offset})
		var result Lines
		if err := rpc.Call(t.Context(), "native/content", RPCParams{Body: body}, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	expected := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	var retained *selectedContent
	var offset uint64
	rowsRead := 0
	pages := 0
	for {
		page := read(401, offset)
		cached := frontends[401].frontend.selected
		if cached == nil || cached.source == "" {
			t.Fatal("large source was not retained")
		}
		if retained == nil {
			retained = cached
		} else if retained != cached {
			t.Fatal("continuation recomposed selected source")
		}
		if !slices.Equal(page.Lines, expected[rowsRead:rowsRead+len(page.Lines)]) {
			t.Fatalf("content changed at offset %d", offset)
		}
		rowsRead += len(page.Lines)
		pages++
		if pages == 2 {
			other := read(402, 0)
			if !slices.Equal(other.Lines, expected[:len(other.Lines)]) || frontends[402].frontend.selected == retained {
				t.Fatal("physical frontends shared mutable selection")
			}
		}
		if page.Next == nil {
			break
		}
		if *page.Next <= offset {
			t.Fatal("nonadvancing continuation")
		}
		offset = *page.Next
	}
	if rowsRead != len(expected) {
		t.Fatal("continuation omitted rows")
	}
	// Complete HTTP resources still return the original public projection.
	body, _ := json.Marshal(RPCParams{Thread: "thread", Body: mustJSON(t, ref)})
	req, _ := http.NewRequestWithContext(t.Context(), "POST", f.server.URL+ResourcePath+"content", bytes.NewReader(body))
	req.Header.Set(ProtocolHeader, ProtocolVersion)
	response, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var full ContentResult
	if response.StatusCode != 200 || json.UnmarshalRead(response.Body, &full) != nil || full.Review == nil || full.Review.Chunks[0].Review.Diff != text {
		t.Fatal("complete resource read changed")
	}
	if frontends[401].frontend.selected != retained {
		t.Fatal("complete resource read evicted selected source")
	}
	t.Logf("%d retained bytes, %d rows, %d pages", len(text), rowsRead, pages)
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestLiveContentReusesSelectedSourceAndRejectsReplacement(t *testing.T) {
	s := NewLive()
	frame := Frame{Session: Session{Thread: "thread", Workspace: "/workspace"}, Attachment: 1, Live: []preview.Preview{{ID: "live", Input: "--- live.go\n+++ live.go\n@@ -1 +1 @@\n-old\n+new\n"}}}
	if err := s.Publish(frame); err != nil {
		t.Fatal(err)
	}
	ref := ContentReference{Attachment: s.readAttachment, Kind: "live", Revision: s.contentVersions["live"], Key: "live"}
	var cached *selectedContent
	if _, failure := s.selectedContent(ref, 0, &cached); failure != nil {
		t.Fatal(failure)
	}
	retained := cached
	if _, failure := s.selectedContent(ref, uint64(strings.IndexByte(cached.source, '\n')+1), &cached); failure != nil || cached != retained {
		t.Fatal("live source rebuilt")
	}
	frame.Live = slices.Clone(frame.Live)
	frame.Live[0].Input += "+later\n"
	if err := s.Publish(frame); err != nil {
		t.Fatal(err)
	}
	if _, failure := s.selectedContent(ref, 1, &cached); failure == nil {
		t.Fatal("stale live continuation admitted")
	}
}

func TestLiveMetadataPageAvoidsSourceTransferAndDecoding(t *testing.T) {
	s := NewLive()
	frame := Frame{Session: Session{Thread: "thread", Workspace: "/workspace"}, Attachment: 1}
	for index := range 60 {
		frame.Live = append(frame.Live, preview.Preview{ID: fmt.Sprint(index), Caller: "/root/worker", Thread: "thread", Workspace: "/workspace", Input: strings.Repeat("source\n", 25000), Files: []diffmodel.ReviewFile{{BeforePath: "a.go", AfterPath: "a.go", Diff: strings.Repeat("raw diff\n", 10000)}}})
	}
	if err := s.Publish(frame); err != nil {
		t.Fatal(err)
	}
	if len(s.livePreviews) <= 7<<20 {
		t.Fatal("fixture does not exceed helper response budget")
	}
	cursor := PageCursor{Attachment: s.readAttachment, Kind: "live", Revision: s.contentVersions["live"]}
	seen := 0
	for {
		value, failure := s.page(mustJSON(t, cursor))
		if failure != nil {
			t.Fatal(failure)
		}
		page := value.(PageResult)
		data := mustJSON(t, page)
		if len(data) > 32<<10 {
			t.Fatal("metadata page exceeded shared envelope budget")
		}
		for _, metadata := range page.Live {
			if metadata.ID != fmt.Sprint(seen) || metadata.Input != "" || len(metadata.Files) != 1 || metadata.Files[0].BeforePath != "a.go" || metadata.Files[0].Diff != "" {
				t.Fatal("metadata page lost identity or transferred raw source")
			}
			seen++
		}
		if page.Next == nil {
			break
		}
		cursor = *page.Next
	}
	if seen != len(frame.Live) {
		t.Fatal("live metadata enumeration incomplete")
	}
	selected := ContentReference{Attachment: s.readAttachment, Kind: "live", Revision: s.contentVersions["live"], Key: "42"}
	full, failure := s.content(mustJSON(t, selected))
	if failure != nil || len(full.(ContentResult).Live) != 1 || full.(ContentResult).Live[0].Input != frame.Live[42].Input {
		t.Fatal("selected read did not borrow complete original source")
	}
	// Cached metadata pages visit only their bounded window, not the raw strings.
	small := testing.AllocsPerRun(3, func() {
		_, _ = livePage(frame.Live[:1], s.readAttachment, selected.Revision, PageCursor{Attachment: s.readAttachment, Kind: "live", Revision: selected.Revision})
	})
	large := testing.AllocsPerRun(3, func() { _, _ = livePage(frame.Live, s.readAttachment, selected.Revision, cursor) })
	if large > small*55+20 {
		t.Fatalf("metadata work scaled with raw source: small=%v large=%v", small, large)
	}
	frame.Live = slices.Clone(frame.Live)
	frame.Live[0].Input += "later"
	if err := s.Publish(frame); err != nil {
		t.Fatal(err)
	}
	if _, failure := s.page(mustJSON(t, cursor)); failure == nil {
		t.Fatal("stale live metadata cursor admitted")
	}
}

func TestLiveMetadataCallerFilterPreservesSourceContinuation(t *testing.T) {
	api := NewLive()
	frame := Frame{Session: Session{Thread: "thread", Workspace: "/workspace"}, Attachment: 1}
	for index := range 80 {
		caller := "/root"
		if index%2 != 0 {
			caller = "/root/worker"
		}
		path := strings.Repeat("p", 400) + ".go"
		frame.Live = append(frame.Live, preview.Preview{ID: fmt.Sprint(index), Caller: caller, Thread: "thread", Input: strings.Repeat("source\n", 10000), Files: []diffmodel.ReviewFile{{BeforePath: path, AfterPath: path}}})
	}
	if err := api.Publish(frame); err != nil {
		t.Fatal(err)
	}
	hub := NewHub(t.Context(), nil)
	registerTestSession(hub, "thread", api, Summary{Thread: "thread", Workspace: "/workspace"})
	server := httptest.NewServer(Versioned(hub.Handler()))
	defer server.Close()
	rpc, err := OpenRPC(t.Context(), server.Client(), server.URL+RPCPath, "")
	if err != nil {
		t.Fatal(err)
	}
	defer rpc.Close()
	cursor := PageCursor{Attachment: api.readAttachment, Kind: "live", Revision: api.contentVersions["live"], Caller: "/root"}
	count, pages := 0, 0
	for {
		var page PageResult
		if err := rpc.Call(t.Context(), "session/page", RPCParams{Thread: "thread", Body: mustJSON(t, cursor)}, &page); err != nil {
			t.Fatal(err)
		}
		if len(mustJSON(t, page)) > 32<<10 {
			t.Fatal("caller-filtered metadata exceeded shared budget")
		}
		for _, metadata := range page.Live {
			if metadata.Caller != cursor.Caller || metadata.ID != fmt.Sprint(2*count) || metadata.Input != "" {
				t.Fatalf("caller-filtered source enumeration changed at %d: %+v", count, metadata)
			}
			count++
		}
		pages++
		if page.Next == nil {
			break
		}
		if page.Next.Caller != cursor.Caller || page.Next.Before <= cursor.Before {
			t.Fatalf("cursor lost caller or source boundary: %+v", page.Next)
		}
		cursor = *page.Next
	}
	if count != len(frame.Live)/2 || pages < 2 {
		t.Fatalf("filtered continuation incomplete or untested: previews=%d pages=%d", count, pages)
	}
}
