// Package tokenizer provides Mekugi's pinned GPT-5 (o200k_base) tokenizer.
//
// The vocabulary is embedded data, not a generated Go map. Token IDs and the
// splitting/BPE algorithm match github.com/tiktoken-go/tokenizer v0.8.1.
// Special-token spellings in source are ordinary text, not control tokens.
package tokenizer

import (
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/dlclark/regexp2/v2"
	_ "github.com/yusing/mekugi/internal/tokenizer/regex"
)

// Codec is the tokenization surface used by capture and CTP.
type Codec interface {
	GetName() string
	Count(string) (int, error)
	Encode(string) ([]uint, []string, error)
	Decode([]uint) (string, error)
}

// Source: github.com/pkoukk/tiktoken-go-loader/assets/o200k_base.tiktoken@v0.0.2
// Original: https://openaipublic.blob.core.windows.net/encodings/o200k_base.tiktoken
// SHA-256 pins the exact bytes; LICENSE.data covers the distributed asset.
//
//go:embed o200k_base.tiktoken
var vocabularyData string

const vocabularySHA256 = "446a9538cb6c348e3516120d7c08b09f57c36495e2acfffe59a5bf8b0cfb1a2d"

// Source: github.com/tiktoken-go/tokenizer/codec/o200k_base.go@v0.8.1
const splitPattern = `[^\r\n\p{L}\p{N}]?[\p{Lu}\p{Lt}\p{Lm}\p{Lo}\p{M}]*[\p{Ll}\p{Lm}\p{Lo}\p{M}]+(?i:'s|'t|'re|'ve|'m|'ll|'d)?|[^\r\n\p{L}\p{N}]?[\p{Lu}\p{Lt}\p{Lm}\p{Lo}\p{M}]+[\p{Ll}\p{Lm}\p{Lo}\p{M}]*(?i:'s|'t|'re|'ve|'m|'ll|'d)?|\p{N}{1,3}| ?[^\s\p{L}\p{N}]+[\r\n/]*|\s*[\r\n]+|\s+(?!\S)|\s+`

type vocabulary struct {
	ranks  map[string]uint
	tokens []string
}

var loadVocabulary = sync.OnceValues(func() (*vocabulary, error) {
	if got := fmt.Sprintf("%x", sha256.Sum256([]byte(vocabularyData))); got != vocabularySHA256 {
		return nil, fmt.Errorf("o200k_base vocabulary checksum: got %s, want %s", got, vocabularySHA256)
	}
	ranks := make(map[string]uint, 199998)
	reverse := make([]string, 0, 199998)
	for line := range strings.SplitSeq(strings.TrimSuffix(vocabularyData, "\n"), "\n") {
		encoded, rank, ok := strings.Cut(line, " ")
		token, err := base64.StdEncoding.DecodeString(encoded)
		id, rankErr := strconv.ParseUint(rank, 10, 32)
		if !ok || err != nil || rankErr != nil || id != uint64(len(reverse)) {
			return nil, fmt.Errorf("invalid o200k_base vocabulary row %d", len(reverse))
		}
		piece := string(token)
		ranks[piece] = uint(id)
		reverse = append(reverse, piece)
	}
	return &vocabulary{ranks: ranks, tokens: reverse}, nil
})

// New returns a codec with immutable shared vocabulary and its own regexp.
func New() (Codec, error) {
	vocabulary, err := loadVocabulary()
	if err != nil {
		return nil, err
	}
	return &codec{
		vocabulary: vocabulary.ranks, reverseVocabulary: vocabulary.tokens,
		splitRegexp: regexp2.MustCompile(splitPattern, regexp2.None),
	}, nil
}
