package router

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestJournalListTransportPagesLargeAuthenticatedSnapshot(t *testing.T) {
	want := make([]journalItem, 100)
	for index := range want {
		want[index] = journalItem{ID: strconv.Itoa(index), Text: strings.Repeat("\x01", maxJournalItemBytes-1), Author: "/root"}
	}
	broker := newCommentaryBroker()
	broker.journalLister = func(_ context.Context, session, thread, agent string) ([]journalItem, error) {
		if session != "session" || thread != "thread" || agent != "/root" {
			t.Fatalf("unexpected list identity: %q %q %q", session, thread, agent)
		}
		return want, nil
	}
	server := httptest.NewServer(http.HandlerFunc(broker.serveHTTP))
	defer server.Close()
	token := broker.subscribe("session", "list-call")
	broker.bindActivity(token, "thread")
	type page struct {
		OK       bool              `json:"ok"`
		Items    []journalListItem `json:"items"`
		Next     *int              `json:"next"`
		Revision string            `json:"revision"`
	}
	var collected []journalListItem
	var revision string
	for offset := 0; ; {
		args := []string{commentaryOnceArgument, server.URL, token, url.PathEscape(`{"op":"list","agent":"/root"}`)}
		if offset != 0 {
			args = append(args, strconv.Itoa(offset), revision)
		}
		var output bytes.Buffer
		matched, err := publishCommentaryOnce(t.Context(), &output, args)
		if !matched || err != nil {
			t.Fatalf("page %d transport: matched=%t err=%v", offset, matched, err)
		}
		if output.Len() >= 1<<20 {
			t.Fatalf("page %d exceeds stock exec collection limit: %d", offset, output.Len())
		}
		var got page
		if err := json.Unmarshal(output.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if !got.OK || len(got.Items) == 0 || len(got.Items) > journalListPageItems || got.Revision == "" {
			t.Fatalf("invalid page %d: %+v", offset, got)
		}
		if revision != "" && got.Revision != revision {
			t.Fatal("revision changed without rejecting the snapshot")
		}
		revision = got.Revision
		collected = append(collected, got.Items...)
		if got.Next == nil {
			break
		}
		if *got.Next != offset+len(got.Items) {
			t.Fatalf("next offset = %d at %d", *got.Next, offset)
		}
		offset = *got.Next
	}
	if len(collected) != len(want) {
		t.Fatalf("collected %d items, want %d", len(collected), len(want))
	}
	for index, item := range collected {
		if item.ID != want[index].ID || item.Text != want[index].Text {
			t.Fatalf("item %d changed", index)
		}
	}
}

func TestJournalListTransportRejectsStaleAndInvalidContinuations(t *testing.T) {
	items := make([]journalItem, journalListPageItems+1)
	for index := range items {
		items[index] = journalItem{ID: strconv.Itoa(index), Text: "original", Author: "/root"}
	}
	broker := newCommentaryBroker()
	broker.journalLister = func(context.Context, string, string, string) ([]journalItem, error) { return items, nil }
	server := httptest.NewServer(http.HandlerFunc(broker.serveHTTP))
	defer server.Close()
	token := broker.subscribe("session", "list-call")
	broker.bindActivity(token, "thread")
	sink := &httpCommentarySink{endpoint: server.URL, token: token, client: server.Client()}
	direct, err := sink.send(t.Context(), map[string]any{"op": "list"})
	if err != nil {
		t.Fatal(err)
	}
	var directList struct {
		Items []journalListItem `json:"items"`
	}
	if err := json.Unmarshal(direct, &directList); err != nil {
		t.Fatal(err)
	}
	if len(directList.Items) != len(items) {
		t.Fatalf("unpaged direct list has %d items", len(directList.Items))
	}
	base := []string{commentaryOnceArgument, server.URL, token, url.PathEscape(`{"op":"list"}`)}
	var first bytes.Buffer
	if matched, err := publishCommentaryOnce(t.Context(), &first, base); !matched || err != nil {
		t.Fatalf("first page: %t %v", matched, err)
	}
	var firstPage struct {
		Next     int    `json:"next"`
		Revision string `json:"revision"`
	}
	if err := json.Unmarshal(first.Bytes(), &firstPage); err != nil {
		t.Fatal(err)
	}
	if firstPage.Next != journalListPageItems || firstPage.Revision == "" {
		t.Fatalf("first page = %s", first.String())
	}
	items[0].Text = "changed"
	for _, continuation := range [][]string{
		{strconv.Itoa(firstPage.Next), firstPage.Revision},
		{"garbage", firstPage.Revision},
		{strconv.Itoa(firstPage.Next), ""},
	} {
		var output bytes.Buffer
		matched, err := publishCommentaryOnce(t.Context(), &output, append(append([]string{}, base...), continuation...))
		if !matched || err == nil {
			t.Fatalf("continuation %v accepted: matched=%t output=%s", continuation, matched, output.String())
		}
	}
	var forbidden bytes.Buffer
	if matched, err := publishCommentaryOnce(t.Context(), &forbidden, []string{commentaryOnceArgument, server.URL, token, url.PathEscape(`{"op":"list","page":0}`)}); !matched || err == nil {
		t.Fatalf("model-supplied page accepted: matched=%t output=%s", matched, forbidden.String())
	}
	var output bytes.Buffer
	if matched, err := publishCommentaryOnce(t.Context(), &output, append(append([]string{}, base...), "1")); matched && err == nil {
		t.Fatalf("incomplete continuation accepted: %s", output.String())
	}
}
