package binding

import (
	"encoding/json/jsontext"
	"reflect"
	"testing"
)

func TestDecodeShellGuardApproval(t *testing.T) {
	got, err := DecodeShellGuardApproval(jsontext.Value(`{
		"threadId": "t1",
		"turnId": "turn-1",
		"itemId": "item-1",
		"startedAtMs": 1700000000123,
		"kind": "remoteWrite",
		"cwd": "/repo",
		"executable": "/usr/bin/git",
		"argv": ["git", "push", "origin", "main"]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	want := ShellGuardApprovalParams{
		ThreadID:    "t1",
		TurnID:      "turn-1",
		ItemID:      "item-1",
		StartedAtMs: 1700000000123,
		Kind:        RemoteWrite,
		Cwd:         "/repo",
		Executable:  "/usr/bin/git",
		Argv:        []string{"git", "push", "origin", "main"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%+v want=%+v", got, want)
	}

	for _, invalid := range []string{
		`{"kind":"unknown","argv":["git","push"]}`,
		`{"kind":"remoteWrite","argv":[]}`,
		`{`,
	} {
		if _, err := DecodeShellGuardApproval(jsontext.Value(invalid)); err == nil {
			t.Fatalf("accepted invalid approval payload: %s", invalid)
		}
	}
}

func TestDecodeShellGuardSecret(t *testing.T) {
	got, err := DecodeShellGuardSecret(jsontext.Value(`{
		"threadId": "t1",
		"turnId": "turn-1",
		"startedAtMs": 1700000000456,
		"kind": "gpgPassphrase",
		"cwd": "/repo",
		"executable": "/usr/bin/gpg",
		"argv": ["gpg", "--sign", "file.txt"]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != GpgPassphrase || got.ThreadID != "t1" || got.StartedAtMs != 1700000000456 || !reflect.DeepEqual(got.Argv, []string{"gpg", "--sign", "file.txt"}) {
		t.Fatalf("got=%+v", got)
	}

	for _, invalid := range []string{
		`{"kind":"remoteWrite","argv":["gpg","--sign"]}`,
		`{"kind":"sudoPassword","argv":[]}`,
		`{`,
	} {
		if _, err := DecodeShellGuardSecret(jsontext.Value(invalid)); err == nil {
			t.Fatalf("accepted invalid secret payload: %s", invalid)
		}
	}
}
