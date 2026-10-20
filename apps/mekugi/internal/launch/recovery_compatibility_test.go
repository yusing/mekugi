package launch

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadRecordCanonicalCompat(t *testing.T) {
	t.Parallel()
	const frozen = `{"changes":{"overlap_labels":{"a":"\u003c\u0026","z":"quote\""},"selected":{"amber1":{"Correlation":"c\u003c","Calls":null},"apple1":{"Correlation":"c2","Calls":[]}},"streams":[{"Thread":"t","Next":2}],"frozen":true,"workspace":"/w\u003c","ids":["apple1","amber1"],"paths":[],"view":"summary","offset":0,"digest":"0000000000000000000000000000000000000000000000000000000000000000"},"version":1,"id":"amber","stdout":"","stderr":"","exit_code":0,"position":[0,0]}`
	const historical = `{"changes":{"workspace":"/w","ids":["amber1"],"paths":null,"view":"","offset":0,"digest":"0000000000000000000000000000000000000000000000000000000000000000"},"version":1,"id":"amber","stdout":"","stderr":"","exit_code":0,"position":[0,0]}`
	for _, tc := range []struct {
		name, input, canonical, binding string
	}{
		{"frozen", frozen, frozen, "0cdb32f8b732ce2c355c9ea662b0fb86f4f5aefed7e851ab6b69709cfd069c22"},
		{"frozen reordered", `{"exit_code":0,"stderr":"","stdout":"","id":"amber","version":1,"changes":{"digest":"0000000000000000000000000000000000000000000000000000000000000000","ids":["apple1","amber1"],"workspace":"/w<","frozen":true,"streams":[{"Next":2,"Thread":"t","Retired":0,"Name":""}],"selected":{"apple1":{"Calls":[],"Correlation":"c2","RetiredCalls":0},"amber1":{"Correlation":"c<"}},"overlap_labels":{"z":"quote\"","a":"<&"},"view":"summary","paths":[]}}`, frozen, "0cdb32f8b732ce2c355c9ea662b0fb86f4f5aefed7e851ab6b69709cfd069c22"},
		{"historical", historical, historical, "d6245add8427b90a7a0856708aee0b8a7313639a2cb81836b3b13e2ec355386e"},
		{"historical omitted defaults", strings.Replace(historical, `"paths":null,`, "", 1), historical, "d6245add8427b90a7a0856708aee0b8a7313639a2cb81836b3b13e2ec355386e"},
		{"historical empty optional collections", strings.Replace(historical, `"changes":{`, `"changes":{"selected":{},"streams":[],"overlap_labels":{},`, 1), historical, "d6245add8427b90a7a0856708aee0b8a7313639a2cb81836b3b13e2ec355386e"},
		{"historical empty paths", strings.Replace(historical, `"paths":null`, `"paths":[]`, 1), strings.Replace(historical, `"paths":null`, `"paths":[]`, 1), "69074daf7bd098848c911310e3d93179f2047307345b5edab1a8a6ef9fa55526"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			var envelope struct {
				Changes json.RawMessage `json:"changes"`
			}
			if err := json.Unmarshal([]byte(tc.input), &envelope); err != nil {
				t.Fatal(err)
			}
			session, err := store.recoverySession(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			handles, err := session.Reserve(t.Context(), 1)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := session.PublishSelectionAt(t.Context(), handles[0], envelope.Changes); err != nil {
				t.Fatal(err)
			}
			paths, err := filepath.Glob(filepath.Join(store.directory, "output-*.json"))
			if err != nil || len(paths) != 1 {
				t.Fatalf("records: %v %v", paths, err)
			}
			data, err := os.ReadFile(paths[0])
			if err != nil || string(data) != tc.canonical {
				t.Fatalf("canonical record = %s, %v; want %s", data, err, tc.canonical)
			}
			if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != tc.binding {
				t.Fatalf("binding = %s; want %s", got, tc.binding)
			}

		})
	}
}
