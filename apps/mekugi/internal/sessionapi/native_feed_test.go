package sessionapi

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	activity "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

func TestNativeEntryDeltasThroughQueuesAndSocket(t *testing.T) {
	for _, backlog := range []string{"server", "client", "socket"} {
		t.Run(backlog, func(t *testing.T) {
			native, client := nativeTestServer(t)
			frame := benchmarkNativeHistoryFrame(16)
			frame.Agents = benchmarkNativeHistoryFrame(8).Main
			var connection *NativeConnection
			var source *rpcSubscription
			var attachment string
			if backlog == "socket" {
				var request NativeRequest
				connection, request = openNativeTest(t, native, client, frame, "")
				attachment = request.Attachment
				if _, err := connection.Next(); err != nil {
					t.Fatal(err)
				}
			} else {
				var request NativeRequest
				var failure *RPCError
				source, request, failure = native.newView(t.Context(), requestTarget{"thread", "/workspace"}, "", localNativeView)
				if failure != nil {
					t.Fatal(failure)
				}
				defer source.close()
				attachment = request.Attachment
			}
			var events []publication
			for step := range 4 {
				frame.View.Version.Revision++
				frame.Main = new(*frame.Main)
				frame.Main.Entries = slices.Clone(frame.Main.Entries)
				frame.Main.Entries[step].Text = fmt.Sprint("changed ", step)
				// Output facts can change without a semantic revision bump.
				frame.Main.Entries[step].OutputFailed = true
				frame.Main.Entries[step].Outputs = map[int]int{0: step + 1}
				frame.Agents = new(*frame.Agents)
				frame.Agents.Entries = slices.Clone(frame.Agents.Entries)
				frame.Agents.Entries[step].Text = "agent changed"
				frame.Agents.MainSummary = []activity.Entry{frame.Main.Entries[step]}
				if step == 3 {
					frame.Main.Entries = frame.Main.Entries[1:]
					slices.Reverse(frame.Main.Entries)
				}
				if err := native.Publish(attachment, frame); err != nil {
					t.Fatal(err)
				}
				if backlog == "client" {
					event, ok := source.native.pop()
					if !ok {
						t.Fatal("missing update")
					}
					if step > 0 {
						var flags struct {
							EntryDeltas nativeOwners `json:"entryDeltas"`
						}
						if err := json.Unmarshal(event.Data, &flags); err != nil || flags.EntryDeltas != nativeMain|nativeAgents {
							t.Fatal("changed owners did not use entry deltas", err)
						}
					}
					events = append(events, event)
				}
				if backlog == "socket" {
					got, err := connection.Next()
					if err != nil {
						t.Fatal(err)
					}
					assertNativeFeedFrame(t, got.Frame, frame)
				}
			}
			if backlog != "socket" {
				if backlog == "server" {
					event, ok := source.native.pop()
					if !ok || event.Kind != "snapshot" {
						t.Fatal("initial snapshot lost")
					}
					events = []publication{event}
				}
				_, subscription := bufferedRPCPublications(t, "view/attach", events)
				connection = &NativeConnection{thread: "thread", subscription: subscription}
				got, err := connection.Next()
				if err != nil {
					t.Fatal(err)
				}
				assertNativeFeedFrame(t, got.Frame, frame)
			}
		})
	}
}

func assertNativeFeedFrame(t *testing.T, got, want NativeFrame) {
	t.Helper()
	a, err := json.Marshal(got, nativeEncoding)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(want.withoutEditors(), nativeEncoding)
	if err != nil || !bytes.Equal(a, b) {
		t.Fatal("entry deltas changed reconstructed frame", err)
	}
}

func TestNativeEntryDeltaValidationAndRawComposition(t *testing.T) {
	prior := benchmarkNativeHistoryFrame(4)
	previous := NativeEvent{Attachment: "view", Frame: prior}
	frozen, err := json.Marshal(previous, nativeEncoding)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []struct {
		flags   nativeOwners
		entries string
		reset   bool
	}{
		{1, `[999]`, false}, {1, `[1,1]`, false}, {1, `["1"]`, false}, {1, `[1.5]`, false},
		{4, `[1]`, false}, {1, `[1]`, true},
	} {
		frame := prior
		frame.Main = nil
		if input.reset {
			frame.View.Version.Attachment++
		}
		metadata, _ := json.Marshal(frame, nativeEncoding)
		var raw map[string]jsontext.Value
		if err := json.Unmarshal(metadata, &raw); err != nil {
			t.Fatal(err)
		}
		raw["main"] = jsontext.Value(`{"Entries":` + input.entries + `}`)
		data, err := json.Marshal(struct {
			Attachment  string                    `json:"attachment"`
			EntryDeltas nativeOwners              `json:"entryDeltas"`
			Frame       map[string]jsontext.Value `json:"frame"`
		}{"view", input.flags, raw}, nativeEncoding)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := decodeNativeEvent("patch", data, previous); err == nil {
			t.Fatalf("invalid delta admitted: %+v", input)
		}
	}
	after, _ := json.Marshal(previous, nativeEncoding)
	if !bytes.Equal(after, frozen) {
		t.Fatal("failed decoding mutated prior owner")
	}
	before := jsontext.Value(`{"Entries":[{"Seq":9007199254740993,"future":9007199254740995},2]}`)
	next := jsontext.Value(`{"Entries":[2,9007199254740993],"future":9007199254740997}`)
	composed, delta, ok := coalesceNativeFeed(before, next)
	if !ok || !delta || !bytes.Contains(composed, []byte(`9007199254740995`)) || !bytes.Contains(composed, []byte(`9007199254740997`)) {
		t.Fatalf("raw facts lost: %s", composed)
	}
}

func TestNativeEntryDeltaCapacityMatchesCompleteState(t *testing.T) {
	frame := benchmarkNativeHistoryFrame(16)
	initial, err := encodeNativeFeed(frame.Main, nativeFeedState{}, false)
	if err != nil {
		t.Fatal(err)
	}
	frame.Main = new(*frame.Main)
	frame.Main.LastSeq++
	main, err := encodeNativeFeed(frame.Main, initial.state, true)
	if err != nil || !main.delta {
		t.Fatal("missing delta", err)
	}
	full, err := json.Marshal(frame.Main, nativeEncoding)
	if err != nil || main.fullBytes != len(full) {
		t.Fatal("complete footprint differs", err)
	}
	agents, err := encodeNativeFeed(nil, nativeFeedState{}, true)
	if err != nil {
		t.Fatal(err)
	}
	feeds := nativeFeedEncoding{main, agents}
	payload := strings.Repeat("x", nativePublicationBytes/2)
	frame.Effects = []NativeEffect{{ID: 1, Restore: &composer.Draft{Text: payload}}, {ID: 2, Restore: &composer.Draft{Text: payload}}}
	var counts []int
	for _, encoding := range []*nativeFeedEncoding{nil, &feeds} {
		data, err := encodeNativePrefix("view", "", frame, 0, new([4]int), encoding)
		if err != nil {
			t.Fatal(err)
		}
		kind := "snapshot"
		if encoding != nil {
			kind = "patch"
		}
		event, err := decodeNativeEvent(kind, data, NativeEvent{Attachment: "view", Frame: frame})
		if err != nil {
			t.Fatal(err)
		}
		counts = append(counts, len(event.Frame.Effects))
	}
	if counts[0] != 1 || counts[1] != counts[0] {
		t.Fatalf("delta changed prefix capacity: %v", counts)
	}
	frame.Effects = nil
	// A single indivisible hydrated result must retain the complete frame's
	// exact capacity, including at the final byte of the publication budget.
	frame.MainOutput = []activity.OutputResult{{Demand: activity.OutputDemand{Source: 1}}}
	frame.Notice = "x"
	small, err := encodeNativePrefix("view", "", frame, 0, new([4]int), nil)
	if err != nil {
		t.Fatal(err)
	}
	frame.Notice = strings.Repeat("x", nativePublicationBytes-len(small)+1)
	for _, encoding := range []*nativeFeedEncoding{nil, &feeds} {
		data, err := encodeNativePrefix("view", "", frame, 0, new([4]int), encoding)
		if err != nil || len(data) > nativePublicationBytes {
			t.Fatal("admissible hydration refused", err)
		}
		var raw struct {
			Frame struct {
				MainOutput []activity.OutputResult `json:"mainOutput"`
			} `json:"frame"`
		}
		if err := json.Unmarshal(data, &raw, nativeEncoding); err != nil || len(raw.Frame.MainOutput) != 1 {
			t.Fatal("indivisible hydration lost", err)
		}
	}
	frame.MainOutput = nil
	frame.Notice = strings.Repeat("x", nativePublicationBytes-main.fullBytes+100)
	if _, err := encodeNativePrefix("view", "", frame, 0, new([4]int), &feeds); !errors.Is(err, errNativePublicationLimit) {
		t.Fatalf("small delta admitted oversized reconnect state: %v", err)
	}
}

func BenchmarkNativeFeedTail(b *testing.B) {
	for _, n := range []int{50, 500, 2000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			native := NewLive().Native()
			source, request, failure := native.newView(b.Context(), requestTarget{"thread", "/workspace"}, "", localNativeView)
			if failure != nil {
				b.Fatal(failure)
			}
			defer source.close()
			frame := benchmarkNativeHistoryFrame(n)
			if err := native.Publish(request.Attachment, frame); err != nil {
				b.Fatal(err)
			}
			source.native.pop()
			var wire int
			b.ReportAllocs()
			for b.Loop() {
				frame.Main = new(*frame.Main)
				frame.Main.Entries = slices.Clone(frame.Main.Entries)
				frame.Main.Entries[n-1].Revision++
				frame.Main.Entries[n-1].Text = fmt.Sprint(frame.Main.Entries[n-1].Revision)
				if err := native.Publish(request.Attachment, frame); err != nil {
					b.Fatal(err)
				}
				event, ok := source.native.pop()
				if !ok {
					b.Fatal("missing tail update")
				}
				wire = len(event.Data)
			}
			b.ReportMetric(float64(wire), "wire-B/frame")
		})
	}
}
