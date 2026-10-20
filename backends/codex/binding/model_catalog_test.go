package binding

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"testing"
)

func TestCodeModeOnlyModelCatalog(t *testing.T) {
	const source = `{"extra":{"number":9007199254740993},"models":[{"slug":"astra","tool_mode":"code_mode","instructions":"keep"},{"slug":"other","tool_mode":"direct"},{"slug":"missing"},{"slug":"null","tool_mode":null},{"slug":"already","tool_mode":"code_mode_only"}]}`
	got, err := CodeModeOnlyModelCatalog([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	var before, after struct {
		Extra  jsontext.Value              `json:"extra"`
		Models []map[string]jsontext.Value `json:"models"`
	}
	if err := json.Unmarshal([]byte(source), &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(got, &after); err != nil {
		t.Fatal(err)
	}
	for _, model := range before.Models {
		model["tool_mode"] = jsontext.Value(`"code_mode_only"`)
	}
	want, _ := json.Marshal(&before, json.Deterministic(true))
	actual, _ := json.Marshal(&after, json.Deterministic(true))
	if !bytes.Equal(want, actual) {
		t.Fatalf("unrelated model metadata changed: %s", got)
	}
	repeated, err := CodeModeOnlyModelCatalog(got)
	if err != nil || !bytes.Equal(repeated, got) {
		t.Fatalf("catalog projection is not idempotent: %v", err)
	}
	for _, invalid := range []string{`null`, `{}`, `{"models":null}`, `{"models":{}}`, `{"models":[null]}`, `{"models":[1]}`} {
		if _, err := CodeModeOnlyModelCatalog([]byte(invalid)); err == nil {
			t.Fatalf("accepted malformed catalog %s", invalid)
		}
	}
}
