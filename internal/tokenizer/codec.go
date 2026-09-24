// Source: github.com/tiktoken-go/tokenizer/codec/codec.go@v0.8.1
// The splitting and byte-pair merge algorithm is retained verbatim; long pieces
// use an equivalent heap merge, and vocabulary storage is owned by tokenizer.go. See LICENSE for the upstream MIT license.
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
			var parts []part
			if len(piece) > longPieceBytes {
				parts = c.mergeLongPiece(piece)
			} else {
				parts = c.mergePairs(piece)
			}

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

// Pieces above this size use mergeLongPiece. The verbatim merge rescans every
// remaining pair per merge, which is quadratic for long words or whitespace.
const longPieceBytes = 4096

// mergeLongPiece selects the same lowest-rank pair as mergePairs, breaking
// ties at the leftmost byte, using a heap and linked part offsets. This is
// OpenAI tiktoken's _byte_pair_merge_large strategy.
func (c *codec) mergeLongPiece(piece string) []part {
	length := len(piece)
	next := make([]int, length)
	previous := make([]int, length)
	pending := make([]uint, length)
	var heap []uint64
	stride := uint64(length) + 1
	push := func(key uint64) {
		index := len(heap)
		heap = append(heap, key)
		for index > 0 {
			parent := (index - 1) / 2
			if heap[parent] <= key {
				break
			}
			heap[index] = heap[parent]
			index = parent
		}
		heap[index] = key
	}
	pop := func() uint64 {
		result, last := heap[0], heap[len(heap)-1]
		heap = heap[:len(heap)-1]
		if len(heap) == 0 {
			return result
		}
		index := 0
		for index*2+1 < len(heap) {
			child := index*2 + 1
			if child+1 < len(heap) && heap[child+1] < heap[child] {
				child++
			}
			if heap[child] >= last {
				break
			}
			heap[index] = heap[child]
			index = child
		}
		heap[index] = last
		return result
	}
	update := func(index int) {
		pending[index] = math.MaxUint
		right := next[index]
		if right >= length {
			return
		}
		if rank, ok := c.vocabulary[piece[index:next[right]]]; ok {
			pending[index] = rank
			push(uint64(rank)*stride + uint64(index))
		}
	}
	for index := range length {
		next[index], previous[index] = index+1, index-1
	}
	for index := range length - 1 {
		update(index)
	}
	for len(heap) > 0 {
		key := pop()
		left, rank := int(key%stride), uint(key/stride)
		if pending[left] != rank {
			continue
		}
		right := next[left]
		next[left] = next[right]
		pending[right] = math.MaxUint
		if next[left] < length {
			previous[next[left]] = left
		}
		update(left)
		if previous[left] >= 0 {
			update(previous[left])
		}
	}
	var parts []part
	for index := 0; index < length; index = next[index] {
		parts = append(parts, part{offset: index, rank: math.MaxUint})
	}
	return append(parts, part{offset: length, rank: math.MaxUint})
}
