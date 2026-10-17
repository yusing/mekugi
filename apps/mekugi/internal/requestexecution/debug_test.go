package requestexecution

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/modules/diagnostics/diagnostic"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/sessioninspection/query"
)

func TestDebugInstructionSelection(t *testing.T) {
	historyService, err := history.NewService(nil)
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := query.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := diagnostic.New(historyService, inspection, nil)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := service.OpenBundle(diagnostic.BundleOptions{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	d := &Debug{Bundle: bundle}
	body := []byte(`{"model":"test","instructions":"exact\n  text\t","input":[{"role":"developer","content":[{"type":"input_text","text":"inherited"}]},{"type":"additional_tools","tools":[{"name":"shell","description":"exact tool"}]},{"role":"user","content":"excluded user"},{"type":"custom_tool_call","input":"excluded script"}]}`)
	headers := http.Header{"Authorization": {"excluded credential"}, "X-Client-Request-Id": {"client-1"}}
	d.instructions(body, body, headers, "session-1", "request-1", 1)
	dump, _ := os.ReadFile(d.Paths()[1])
	var got struct {
		Instructions   string            `json:"instructions"`
		Developers     []json.RawMessage `json:"developer_messages"`
		Additional     []json.RawMessage `json:"additional_tools"`
		Cached         int               `json:"cached_input_items"`
		Scope          string            `json:"scope"`
		WireDevelopers []json.RawMessage `json:"wire_developer_messages"`
	}
	if err := json.Unmarshal(dump, &got); err != nil || got.Instructions != "exact\n  text\t" || len(got.Developers) != 1 || len(got.Additional) != 1 || got.Cached != 1 {
		t.Fatalf("instruction selection: %+v, %v", got, err)
	}
	if bytes.Contains(dump, []byte("excluded")) {
		t.Fatal("dump retained data outside instruction scope")
	}
	if got.Scope != "projected_responses_request" || !responses.SameJSONValue(responses.MustMarshalJSON(got.WireDevelopers), responses.MustMarshalJSON(got.Developers)) {
		t.Fatal("dump does not distinguish projection from wire instructions")
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
}
