package binding

import (
	"bytes"
	"encoding/json/jsontext"
	"reflect"
	"testing"
	"time"
)

func TestNotificationCommandAndIdentity(t *testing.T) {
	for _, itemID := range []string{"", "outer"} {
		raw := jsontext.Value(`{"threadId":"thread","turnId":"turn","itemId":"` + itemID + `","item":{"id":"nested","type":"commandExecution","command":"printf 'exact\\n'","aggregatedOutput":"","exitCode":0}}`)
		before := bytes.Clone(raw)
		p, err := DecodeNotification("item/completed", raw)
		if err != nil {
			t.Fatal(err)
		}
		at := time.Unix(42, 0)
		e := p.Observation("item/completed", at)
		want := itemID
		if want == "" {
			want = "nested"
		}
		if p.Command == nil || p.Command.Key != [3]string{"thread", "turn", want} || e.ItemID != want || e.At != at || e.Item.Command != "printf 'exact\\n'" || e.Item.ExitCode == nil || *e.Item.ExitCode != 0 || e.Item.AggregatedOutput == nil || *e.Item.AggregatedOutput != "" {
			t.Fatalf("lost native command facts: %+v %+v", p.Command, e)
		}
		if !bytes.Equal(raw, before) {
			t.Fatal("decoding changed original input")
		}
	}
	p, err := DecodeNotification("item/completed", jsontext.Value(`{"item":{"type":"commandExecution"}}`))
	if err != nil || p.Command == nil || p.Command.Exit != nil || p.Command.Output != nil {
		t.Fatalf("unknown outcome became known: %+v %v", p, err)
	}
	if _, err := DecodeNotification("item/started", jsontext.Value(`{"item":`)); err == nil {
		t.Fatal("malformed notification accepted")
	}
}

func TestNotificationPublicTranscriptAndContext(t *testing.T) {
	p, err := DecodeNotification("item/completed", jsontext.Value(`{"threadId":"thread","turnId":"turn","item":{"id":"reason","type":"reasoning","text":"private","content":[{"text":"private"}],"summary":["public"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	e := p.Observation("item/completed", time.Unix(42, 0))
	text := RetainedTranscript(e)
	if e.Item.Text != "" || len(e.Item.Content) != 0 || text.Item.Text != "" || !reflect.DeepEqual(text.Item.Summary, []string{"public"}) || text.Item.Ref.ItemID != "reason" {
		t.Fatalf("public projection: %+v %+v", e, text)
	}
	// Published slices cannot mutate the native envelope retained by application control.
	e.Item.Summary[0] = "changed"
	if p.Item.Summary[0] != "public" {
		t.Fatal("projection borrowed source state")
	}
	for _, last := range []string{"null", `{"totalTokens":0}`} {
		p, err = DecodeNotification("thread/tokenUsage/updated", jsontext.Value(`{"tokenUsage":{"modelContextWindow":100000,"last":`+last+`}}`))
		if err != nil {
			t.Fatal(err)
		}
		context := p.Observation("thread/tokenUsage/updated", time.Time{}).Context
		if context.Window != 100000 || context.Tokens != 0 || context.Known != (last != "null") {
			t.Fatalf("context presence lost: %+v", context)
		}
	}
}

func TestNotificationControlAndHookEnvelope(t *testing.T) {
	p, err := DecodeNotification("turn/completed", jsontext.Value(`{"threadId":"thread","turnId":"outer","turn":{"id":"nested","status":"failed","error":{"message":"host failure"}},"thread":{"id":"thread","cwd":"/workspace","agentNickname":"reader"}}`))
	if err != nil {
		t.Fatal(err)
	}
	e := p.Observation("turn/completed", time.Time{})
	if e.TurnID != "nested" || e.TurnStatus != "failed" || p.Turn.Error == nil || p.Turn.Error.Message != "host failure" || p.Thread.Cwd != "/workspace" || e.Thread.Nickname != "reader" {
		t.Fatalf("control facts lost: %+v %+v", p, e)
	}
	p, err = DecodeNotification("hook/completed", jsontext.Value(`{"threadId":"thread","turnId":"turn","run":{"id":"hook","entries":[{"kind":"error","text":"hook failure"}]}}`))
	if err != nil || p.Run == nil || p.Run.ID != "hook" || len(p.Run.Entries) != 1 || p.Run.Entries[0].Text != "hook failure" {
		t.Fatalf("hook envelope lost: %+v %v", p, err)
	}
}

func TestNotificationRecoverableTurnFailure(t *testing.T) {
	for _, tc := range []struct {
		info string
		want bool
	}{
		{`"serverOverloaded"`, true}, {`"internalServerError"`, true},
		{`{"responseStreamConnectionFailed":{"httpStatusCode":503}}`, true},
		{`{"responseStreamDisconnected":{"httpStatusCode":null}}`, true},
		{`{"responseTooManyFailedAttempts":{"httpStatusCode":502}}`, true},
		{`{"responseStreamConnectionFailed":{"httpStatusCode":401}}`, false},
		{`{"responseTooManyFailedAttempts":{"httpStatusCode":429}}`, false},
		{`"unauthorized"`, false}, {`"usageLimitExceeded"`, false},
		{`"badRequest"`, false}, {`"futureError"`, false}, {`null`, false},
	} {
		t.Run(tc.info, func(t *testing.T) {
			p, err := DecodeNotification("turn/completed", []byte(`{"threadId":"thread","turn":{"id":"turn","status":"failed","error":{"message":"failure detail","codexErrorInfo":`+tc.info+`}}}`))
			if err != nil || p.Turn.Error == nil || p.Turn.Error.Message != "failure detail" || p.Turn.Error.Recoverable() != tc.want || string(p.Turn.Error.CodexErrorInfo) != tc.info {
				t.Fatalf("failure classification/evidence: %+v %v", p.Turn.Error, err)
			}
		})
	}
}
