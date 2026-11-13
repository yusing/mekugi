package appserver

import (
	"bytes"
	json "encoding/json/v2"
	"testing"
)

func TestProgressWireKeepsExplicitRecoveryOnlyAcknowledgements(t *testing.T) {
	response := ProgressOutputResponse{Text: "mread retained", RecoveryRef: new("retained")}
	data, err := json.Marshal(&response)
	if err != nil || !bytes.Contains(data, []byte(`"deliveredIntervals":[]`)) || !bytes.Contains(data, []byte(`"recoveryRef":"retained"`)) {
		t.Fatalf("recovery-only wire=%s err=%v", data, err)
	}
	interval := CaptureInterval{Key: "opaque", ChangeID: "amber1", RecordOrder: 1, FirstChunk: 1, LastChunk: 2}
	data, err = json.Marshal(&interval)
	if err != nil || !bytes.Contains(data, []byte(`"legacyRecordKey":null`)) {
		t.Fatalf("optional interval identity wire=%s err=%v", data, err)
	}
}
