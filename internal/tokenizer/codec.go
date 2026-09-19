// Source: github.com/tiktoken-go/tokenizer/codec/codec.go@v0.8.1
// The splitting and byte-pair merge algorithm is retained verbatim; vocabulary
// storage is owned by tokenizer.go. See LICENSE for the upstream MIT license.
package tokenizer

import (
	"fmt"
	"math"
	"strings"

	"github.com/dlclark/regexp2/v2"
)

type codec struct {
	vocabulary        map[string]uint
	reverseVocabulary []string
	splitRegexp       *regexp2.Regexp
}

func (c *codec) GetName() string {
	return "o200k_base"
}

// Count returns the number of tokens in the input string.
func (c *codec) Count(input string) (int, error) {
	var count int

	err := c.tokenize(input, func(_ uint, _ string) {
		count++
	})

	return count, err
}

// Encode returns the token IDs and tokens for the input string.
func (c *codec) Encode(input string) ([]uint, []string, error) {

	var ids []uint
	var tokens []string

	err := c.tokenize(input, func(id uint, token string) {
		ids = append(ids, id)
		tokens = append(tokens, token)
	})

	return ids, tokens, err
}

func (c *codec) tokenize(input string, yield func(uint, string)) error {
	match, err := c.splitRegexp.FindStringMatch(input)
	if err != nil {
		return fmt.Errorf("error matching: %v", err)
	}
	for match != nil {
		piece := match.String()
		if id, ok := c.vocabulary[piece]; ok {
			yield(id, piece)
		} else {
			parts := c.mergePairs(piece)

			for i := range len(parts) - 1 {
				token := piece[parts[i].offset:parts[i+1].offset]
				yield(c.vocabulary[token], token)
			}
		}
		match, err = c.splitRegexp.FindNextMatch(match)
		if err != nil {
			return fmt.Errorf("error matching: %v", err)
		}
	}

	return nil
}

func (c *codec) Decode(tokens []uint) (string, error) {
	var out strings.Builder
	for _, t := range tokens {
		if t >= uint(len(c.reverseVocabulary)) {
			return "", fmt.Errorf("invalid token: %d", t)
		}
		out.WriteString(c.reverseVocabulary[t])
	}
	return out.String(), nil
}

type part struct {
	offset int
	rank   uint
}

func (c *codec) mergePairs(piece string) []part {
	parts := make([]part, len(piece)+1)
	for i := range len(parts) {
		parts[i] = part{i, math.MaxUint}
	}

	getRank := func(index, skip int) uint {
		if index+skip+2 < len(parts) {
			start := parts[index].offset
			end := parts[index+skip+2].offset
			if rank, ok := c.vocabulary[piece[start:end]]; ok {
				return rank
			}
		}
		return math.MaxUint
	}

	for i := 0; i < len(parts)-2; i++ {
		parts[i].rank = getRank(i, 0)
	}

	for {
		if len(parts) == 1 {
			break
		}

		minRank := uint(math.MaxUint)
		minIndex := 0
		for i, p := range parts[:len(parts)-1] {
			if p.rank < minRank {
				minRank = p.rank
				minIndex = i
			}
		}

		if minRank == math.MaxUint {
			break
		}

		parts[minIndex].rank = getRank(minIndex, 1)

		if minIndex > 0 {
			parts[minIndex-1].rank = getRank(minIndex-1, 1)
		}

		parts = append(parts[:minIndex+1], parts[minIndex+2:]...)
	}

	return parts
}
