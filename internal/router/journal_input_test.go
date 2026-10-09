package router

import (
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestJournalNativeInputsAreOperationSpecific(t *testing.T) {
	var input struct {
		Defs  map[string]jsonv1.RawMessage `json:"$defs"`
		Items struct {
			AnyOf []struct {
				AdditionalProperties bool                         `json:"additionalProperties"`
				Properties           map[string]jsonv1.RawMessage `json:"properties"`
				Required             []string                     `json:"required"`
			} `json:"anyOf"`
		} `json:"items"`
	}
	if err := json.Unmarshal(mustMarshalJSON(journalMutationsSchemaAt("#/properties/mutations")), &input); err != nil {
		t.Fatal(err)
	}

	want := []struct {
		op, fields, required string
	}{
		{"plan", "op under tasks reset", "op tasks"},
		{"add", "op under kind title body before state reason agent", "op kind title"},
		{"add", "op under kind title body before", "op title"},
		{"set", "op p title body state reason agent superseded_by", "op p"},
		{"log", "op p text", "op text"},
		{"remove", "op p", "op p"},
		{"finish", "op", "op"},
	}
	if len(input.Items.AnyOf) != len(want) {
		t.Fatalf("mutation variants = %d, want %d", len(input.Items.AnyOf), len(want))
	}
	for i, variant := range input.Items.AnyOf {
		var operation struct {
			Enum []string `json:"enum"`
		}
		if err := json.Unmarshal(variant.Properties["op"], &operation); err != nil || !reflect.DeepEqual(operation.Enum, []string{want[i].op}) {
			t.Fatalf("variant %d op = %v, err=%v", i, operation.Enum, err)
		}
		var names []string
		for name := range variant.Properties {
			names = append(names, name)
		}
		expected := strings.Fields(want[i].fields)
		slices.Sort(names)
		slices.Sort(expected)
		if variant.AdditionalProperties || !reflect.DeepEqual(names, expected) || !reflect.DeepEqual(variant.Required, strings.Fields(want[i].required)) {
			t.Fatalf("variant %d is not a closed operation shape: %+v", i, variant)
		}
	}
	for i, kinds := range map[int][]string{1: {"task"}, 2: {"note", "context"}} {
		var kind struct {
			Enum []string `json:"enum"`
		}
		if err := json.Unmarshal(input.Items.AnyOf[i].Properties["kind"], &kind); err != nil || !reflect.DeepEqual(kind.Enum, kinds) {
			t.Fatalf("add variant %d kinds = %v, err=%v", i, kind.Enum, err)
		}
	}
	var plan struct {
		AdditionalProperties bool                         `json:"additionalProperties"`
		Properties           map[string]jsonv1.RawMessage `json:"properties"`
		Required             []string                     `json:"required"`
	}
	if err := json.Unmarshal(input.Defs["task"], &plan); err != nil {
		t.Fatal(err)
	}
	if plan.AdditionalProperties || !reflect.DeepEqual(plan.Required, []string{"title"}) || len(plan.Properties) != 6 || plan.Properties["agent"] != nil {
		t.Fatalf("planned task input is not closed: %+v", plan)
	}
	for _, tasks := range []jsonv1.RawMessage{input.Items.AnyOf[0].Properties["tasks"], plan.Properties["tasks"]} {
		var array struct {
			Items struct {
				AnyOf []struct {
					Ref string `json:"$ref"`
				} `json:"anyOf"`
			} `json:"items"`
		}
		if err := json.Unmarshal(tasks, &array); err != nil || len(array.Items.AnyOf) != 2 || array.Items.AnyOf[1].Ref != "#/properties/mutations/$defs/task" {
			t.Fatalf("planned task reference is not rooted at the projected tool input: %s, err=%v", tasks, err)
		}
	}
}
