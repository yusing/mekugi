package tokenizer

import (
	"bytes"
	"encoding/json/v2"
	"os"
	"reflect"
	"sync"
	"testing"
)

// Generated with github.com/tiktoken-go/tokenizer v0.8.1 before removing that
// dependency. Byte slices retain invalid UTF-8 and control-character cases.
func TestPinnedTokenIDs(t *testing.T) {
	data, err := os.ReadFile("testdata/o200k_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Text    []byte
		IDs     []uint
		Pieces  [][]byte
		Decoded []byte
	}
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	codec, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for i, fixture := range fixtures {
		ids, pieces, err := codec.Encode(string(fixture.Text))
		if err != nil || !reflect.DeepEqual(ids, fixture.IDs) {
			t.Fatalf("fixture %d IDs = %v, want %v: %v", i, ids, fixture.IDs, err)
		}
		if len(pieces) != len(fixture.Pieces) {
			t.Fatalf("fixture %d pieces length = %d, want %d", i, len(pieces), len(fixture.Pieces))
		}
		for j, piece := range pieces {
			if !bytes.Equal([]byte(piece), fixture.Pieces[j]) {
				t.Fatalf("fixture %d piece %d differs", i, j)
			}
		}
		if count, err := codec.Count(string(fixture.Text)); err != nil || count != len(fixture.IDs) {
			t.Fatalf("fixture %d count = %d, want %d: %v", i, count, len(fixture.IDs), err)
		}
		if decoded, err := codec.Decode(ids); err != nil || !bytes.Equal([]byte(decoded), fixture.Decoded) {
			t.Fatalf("fixture %d decoded = %q, want %q: %v", i, decoded, fixture.Decoded, err)
		}
	}
}

func TestVocabularyAndConcurrentCodecs(t *testing.T) {
	vocabulary, err := loadVocabulary()
	if err != nil {
		t.Fatal(err)
	}
	if len(vocabulary.ranks) != 199998 || len(vocabulary.tokens) != 199998 {
		t.Fatalf("vocabulary sizes = %d, %d", len(vocabulary.ranks), len(vocabulary.tokens))
	}
	for id, token := range vocabulary.tokens {
		if rank, ok := vocabulary.ranks[token]; !ok || rank != uint(id) {
			t.Fatalf("rank mismatch at %d", id)
		}
	}
	shared, err := New()
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			own, err := New()
			if err != nil {
				t.Error(err)
				return
			}
			for _, codec := range []Codec{own, shared} {
				for range 20 {
					ids, _, err := codec.Encode("Hello, 世界🙂")
					if err != nil {
						t.Error(err)
						return
					}
					text, err := codec.Decode(ids)
					if err != nil || text != "Hello, 世界🙂" || codec.GetName() != "o200k_base" {
						t.Errorf("round trip %q: %v", text, err)
					}
				}
			}
		})
	}
	workers.Wait()
	for _, id := range []uint{199998, 199999, 200018, ^uint(0)} {
		if _, err := shared.Decode([]uint{id}); err == nil {
			t.Fatalf("accepted invalid token %d", id)
		}
	}
}
