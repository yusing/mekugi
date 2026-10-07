// Package outputdedupe finds repeated line runs in earlier visible text.
// An Index belongs to one forward pass over a model request, not to a session.
package outputdedupe

import (
	"bytes"
	"encoding/binary"
	"hash/maphash"
	"strings"

	"github.com/yusing/goutils/synk"
)

const (
	Threshold   = 192
	WindowBytes = 256 << 10
)

// Span replaces text[Start:End] with one marker row. Source identifies an earlier
// unit; FirstLine and LastLine count its delivered rows, including marker rows.
// Whole means the span covers that entire earlier delivered unit.
type Span struct {
	Start, End          int
	Source              int
	FirstLine, LastLine int
	Whole               bool
}

// Projection retains original text for Add. Callers must leave Spans unchanged.
// Render each span as one marker row, preserving its trailing LF when present.
type Projection struct {
	Spans []Span
	text  string
}

// A piece is one uninterrupted visible run. Text and sparse seed metadata use
// separate buffers to avoid crossing a pool size tier for a few seed bytes.
// Each seed uses a uint32 byte offset and uint64 delivered row.
const seedBytes = 12

type piece struct {
	text, seeds      []byte
	start, firstSeed int
	source, rows     int
	order            uint64
}

func (p *piece) seedCount() int { return len(p.seeds) / seedBytes }
func (p *piece) seed(i int) (offset, row int) {
	b := p.seeds[i*seedBytes:]
	return int(binary.LittleEndian.Uint32(b)), int(binary.LittleEndian.Uint64(b[4:]))
}

// Positions contain numeric IDs, so the hash table does not retain Go pointers.
// IDs remain stable when the front of the piece slice is evicted.
type position struct {
	piece uint64
	seed  int
}

// Index retains at most WindowBytes of referenceable text, plus seed metadata.
// It is not concurrent-safe. Seedless text advances the window without storage.
// Call Close after the forward pass to return its buffers to the pool.
type Index struct {
	seed       maphash.Seed
	seeds      map[uint64][4]position
	pieces     []*piece
	firstPiece uint64
	bytes      uint64
}

func New() *Index {
	return &Index{seed: maphash.MakeSeed(), seeds: make(map[uint64][4]position), firstPiece: 1}
}

// Close clears indexed text before returning buffers. It is safe to call repeatedly.
// Existing projections own only original text and remain usable.
func (idx *Index) Close() {
	for i := range idx.pieces {
		idx.pieces[i].release()
	}
	idx.pieces = nil
	clear(idx.seeds)
	idx.firstPiece, idx.bytes = 1, 0
}

// goutils' smallest sized tier is 2 KiB. Tiny verbatim tails must not each
// retain two full tiers; Put drops these exact-sized sub-tier buffers.
func referenceBuffer(size int) []byte {
	if size < 2<<10 {
		return make([]byte, size)
	}
	return synk.GetSizedBytesPool().GetSized(size)
}

func (p *piece) release() {
	clear(p.text)
	clear(p.seeds)
	synk.GetSizedBytesPool().Put(p.text)
	synk.GetSizedBytesPool().Put(p.seeds)
	*p = piece{}
}

func lineEnd(text string, start int) int {
	if n := strings.IndexByte(text[start:], '\n'); n >= 0 {
		return start + n + 1
	}
	return len(text)
}

func byteLineEnd(text []byte, start int) int {
	if n := bytes.IndexByte(text[start:], '\n'); n >= 0 {
		return start + n + 1
	}
	return len(text)
}

func rowCount(text string) int {
	rows := strings.Count(text, "\n")
	if len(text) > 0 && text[len(text)-1] != '\n' {
		rows++
	}
	return rows
}

func isSeed(content string) bool {
	nonspace := 0
	for i := 0; i < len(content); i++ {
		switch content[i] {
		case ' ', '\t', '\r', '\v', '\f', '\n':
		default:
			nonspace++
			if nonspace == 8 {
				return true
			}
		}
	}
	return false
}

func hashText(seed maphash.Seed, text string) uint64 {
	if strings.HasSuffix(text, "\n") {
		text = strings.TrimSuffix(strings.TrimSuffix(text, "\n"), "\r")
	}
	return maphash.String(seed, text)
}

func equalLine(text string, reference []byte) bool {
	text = strings.TrimSuffix(text, "\n")
	if len(reference) > 0 && reference[len(reference)-1] == '\n' {
		reference = reference[:len(reference)-1]
	}
	return text == string(reference)
}

// Project streams line boundaries without per-line scratch storage. Equal-length
// candidates select the most recent source position, independently of hash seed.
// Units below Threshold are neither matched nor subsequently indexed by Add.
func (idx *Index) Project(text string) Projection {
	result := Projection{text: text}
	if len(text) < Threshold {
		return result
	}
	floor := 0
	for at := 0; at < len(text); {
		next := lineEnd(text, at)
		if !isSeed(text[at:next]) {
			at = next
			continue
		}
		var best Span
		var latest uint64
		for _, pos := range idx.seeds[hashText(idx.seed, text[at:next])] {
			if pos.piece == 0 {
				continue
			}
			p := idx.pieces[pos.piece-idx.firstPiece]
			reference := p.text
			offset, row := p.seed(pos.seed)
			from, to := offset, byteLineEnd(reference, offset)
			if !equalLine(text[at:next], reference[from:to]) {
				continue
			}
			lo, hi, first, last := at, next, row, row
			for lo > floor && from > p.start {
				previous := p.start + bytes.LastIndexByte(reference[p.start:from-1], '\n') + 1
				start := floor + strings.LastIndexByte(text[floor:lo-1], '\n') + 1
				if !equalLine(text[start:lo], reference[previous:from]) {
					break
				}
				lo, from, first = start, previous, first-1
			}
			for hi < len(text) && to < len(reference) {
				end, refEnd := lineEnd(text, hi), byteLineEnd(reference, to)
				if !equalLine(text[hi:end], reference[to:refEnd]) {
					break
				}
				hi, to, last = end, refEnd, last+1
			}
			recency := p.order + uint64(from)
			if hi-lo < Threshold || hi-lo < best.End-best.Start || (hi-lo == best.End-best.Start && recency <= latest) {
				continue
			}
			best = Span{lo, hi, p.source, first, last, first == 1 && last == p.rows}
			latest = recency
		}
		if best.End != 0 {
			result.Spans = append(result.Spans, best)
			floor, next = best.End, best.End
		}
		at = next
	}
	return result
}

// Add indexes only the verbatim rows of a projection made by this Index.
// Source is returned in future spans and should identify this unit to the caller.
// Marker rows and replaced text cannot become reference targets.
func (idx *Index) Add(source int, projection Projection) {
	text := projection.text
	if len(text) < Threshold {
		return
	}
	rows := rowCount(text)
	for _, span := range projection.Spans {
		rows -= rowCount(text[span.Start:span.End]) - 1
	}
	start, row := 0, 1
	for _, span := range projection.Spans {
		if span.Start > start {
			idx.addRun(source, rows, text[start:span.Start], row)
			row += rowCount(text[start:span.Start])
		}
		start, row = span.End, row+1
	}
	if start < len(text) {
		idx.addRun(source, rows, text[start:], row)
	}
}

func (idx *Index) addRun(source, rows int, text string, firstRow int) {
	order := idx.bytes
	idx.bytes += uint64(len(text))
	idx.evict()
	// Keep only whole lines within the byte window, even for an oversized run.
	start := max(0, len(text)-WindowBytes)
	if start > 0 && text[start-1] != '\n' {
		start = lineEnd(text, start)
	}
	firstRow += rowCount(text[:start])
	text = text[start:]
	count := 0
	for at := 0; at < len(text); {
		end := lineEnd(text, at)
		if isSeed(text[at:end]) {
			count++
		}
		at = end
	}
	if count == 0 {
		return
	}
	p := &piece{text: referenceBuffer(len(text)), seeds: referenceBuffer(count * seedBytes),
		source: source, rows: rows, order: order + uint64(start)}
	copy(p.text, text)
	id := idx.firstPiece + uint64(len(idx.pieces))
	seed, row := 0, firstRow
	for at := 0; at < len(text); row++ {
		end := lineEnd(text, at)
		if isSeed(text[at:end]) {
			b := p.seeds[seed*seedBytes:]
			binary.LittleEndian.PutUint32(b, uint32(at))
			binary.LittleEndian.PutUint64(b[4:], uint64(row))
			hash := hashText(idx.seed, text[at:end])
			positions := idx.seeds[hash]
			copy(positions[1:], positions[:3])
			positions[0] = position{id, seed}
			idx.seeds[hash] = positions
			seed++
		}
		at = end
	}
	idx.pieces = append(idx.pieces, p)
}

func (idx *Index) evict() {
	if idx.bytes <= WindowBytes {
		return
	}
	cutoff := idx.bytes - WindowBytes
	for len(idx.pieces) > 0 {
		p := idx.pieces[0]
		if p.order+uint64(p.start) >= cutoff {
			break
		}
		text := p.text
		start := min(int(cutoff-p.order), len(text))
		if start > 0 && text[start-1] != '\n' {
			start = byteLineEnd(text, start)
		}
		p.start = start
		for p.firstSeed < p.seedCount() {
			offset, _ := p.seed(p.firstSeed)
			if offset >= start {
				break
			}
			end := byteLineEnd(text, offset)
			content := text[offset:end]
			if content[len(content)-1] == '\n' {
				content = content[:len(content)-1]
				if len(content) > 0 && content[len(content)-1] == '\r' {
					content = content[:len(content)-1]
				}
			}
			hash := maphash.Bytes(idx.seed, content)
			positions := idx.seeds[hash]
			for i, pos := range positions {
				if pos.piece == idx.firstPiece && pos.seed == p.firstSeed {
					copy(positions[i:], positions[i+1:])
					positions[3] = position{}
					break
				}
			}
			if positions[0].piece == 0 {
				delete(idx.seeds, hash)
			} else {
				idx.seeds[hash] = positions
			}
			p.firstSeed++
		}
		if p.firstSeed < p.seedCount() {
			break
		}
		p.release()
		idx.pieces[0] = nil
		idx.pieces = idx.pieces[1:]
		idx.firstPiece++
	}
}
