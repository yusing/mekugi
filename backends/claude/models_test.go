package claude

import (
	"encoding/json/jsontext"
	"reflect"
	"testing"
)

func TestClientModels(t *testing.T) {
	client := &Client{initialization: jsontext.Value(`{"models":[{"value":"sonnet","displayName":"Sonnet","description":"Native choice","resolvedModel":"claude-sonnet-native","unknown":"ignored"}],"account":{"email":"private@example.invalid"}}`)}
	want := []Model{{Value: "sonnet", DisplayName: "Sonnet", Description: "Native choice", ResolvedModel: "claude-sonnet-native"}}
	models, err := client.Models()
	if err != nil || !reflect.DeepEqual(models, want) {
		t.Fatalf("model metadata = %+v, %v", models, err)
	}
	models[0].Value = "changed"
	models, err = client.Models()
	if err != nil || !reflect.DeepEqual(models, want) {
		t.Fatalf("model metadata was not detached: %+v, %v", models, err)
	}
	for _, raw := range []string{`{}`, `{"models":null}`, `{"models":[{}]}`, `{"models":[null]}`, `{"models":[{"value":12}]}`} {
		t.Run(raw, func(t *testing.T) {
			client := &Client{initialization: jsontext.Value(raw)}
			if models, err := client.Models(); err == nil || models != nil {
				t.Fatalf("invalid catalog admitted: %+v, %v", models, err)
			}
		})
	}
	client = &Client{initialization: jsontext.Value(`{"models":[]}`)}
	if models, err := client.Models(); err != nil || len(models) != 0 {
		t.Fatalf("empty catalog = %+v, %v", models, err)
	}
}
