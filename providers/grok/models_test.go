package grok

import (
	"slices"
	"testing"
)

func TestModelsAreCallerOwned(t *testing.T) {
	models := Models()
	want := slices.Clone(models)
	models[0] = "caller-edited"
	if !slices.Equal(Models(), want) {
		t.Fatal("caller mutation changed supported models")
	}
}
