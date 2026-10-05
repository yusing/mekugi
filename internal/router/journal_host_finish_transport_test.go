package router

import (
	"bytes"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestJournalHelperFinishBatchPersistsOnlyAcceptedReceipt(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		mutations []journalMutation
		want      bool
	}{
		{"finish only", []journalMutation{{Op: "finish"}}, true},
		{"final report", []journalMutation{{Op: "add", Title: new("Checks passed")}, {Op: "finish"}}, true},
		{"finish is not last", []journalMutation{{Op: "finish"}, {Op: "add", Title: new("must roll back")}}, false},
		{"unsupported finish operand", []journalMutation{{Op: "add", Title: new("must roll back")}, {Op: "finish", Title: new("ignored")}}, false},
		{"rejected mutation", []journalMutation{{Op: "set", P: "/missing", State: new("done")}, {Op: "finish"}}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			transform, proxy := newRuntimeCommentaryTransform(t)
			transform.shellTurnID = "final-turn"
			token := testRuntimeCommentaryCall(t, transform, "final-work")
			body, err := json.Marshal(map[string]any{"journal": test.mutations, "id": "publication"})
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, commentaryPublisherPath, bytes.NewReader(body))
			request.Header.Set("Authorization", "Bearer "+token)
			response := httptest.NewRecorder()
			proxy.commentary.serveHTTP(response, request)
			var outcome struct {
				OK    bool     `json:"ok"`
				Items []string `json:"items"`
			}
			if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &outcome) != nil || outcome.OK != test.want {
				t.Fatalf("publication = %d %s", response.Code, response.Body)
			}
			if test.want && outcome.Items == nil {
				t.Fatal("finish must return an empty path array, not null")
			}
			found := false
			if err := proxy.journals.transaction(t.Context(), proxy.replayStore, transform.directory, transform.shellThreadID, func(j *threadJournal, exists bool) error {
				_, found = j.Receipts["runtime:"+journalHostFinishReceipt(transform.shellTurnID, "final-work")]
				if !test.want && len(j.Items) != 0 {
					t.Fatalf("rejected completion leaked journal mutations: %+v", j.Items)
				}
				return errJournalUnchanged
			}); err != nil {
				t.Fatal(err)
			}
			if found != test.want {
				t.Fatalf("finish receipt = %v, want %v", found, test.want)
			}
		})
	}
}
