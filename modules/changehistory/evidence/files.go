package evidence

import (
	"context"
	"errors"
	"unicode/utf8"

	"github.com/yusing/mekugi/libraries/diffmodel"
)

type FileKind string

// File retains endpoint metadata without requiring its text to stay resident.
type File struct {
	Origin        string `json:",omitempty"`
	AlsoManaged   string `json:",omitempty"`
	Path          string
	Kind          FileKind `json:",omitempty"`
	Content       string   `json:",omitempty"`
	ContentRef    *Text    `json:",omitempty"`
	Size          int64    `json:",omitzero"`
	Hash          string   `json:",omitempty"`
	Link          string   `json:",omitempty"`
	Stamp         string   `json:",omitempty"`
	Error         string   `json:",omitempty"`
	WatchStamp    string   `json:"-"`
	WatchMoveOnly bool     `json:"-"`
	CopyOf        string   `json:",omitempty"`
}

func (f File) Text() (string, error) {
	if f.ContentRef != nil {
		return f.ContentRef.Read()
	}
	return f.Content, nil
}

func (f File) TextSize() int {
	if f.ContentRef != nil {
		return f.ContentRef.Size
	}
	return len(f.Content)
}

func (f File) TextDigest() string {
	if f.ContentRef != nil {
		return f.ContentRef.Digest
	}
	return digest([]byte(f.Content))
}

func (f *File) Spill(ctx context.Context, source *Source) error {
	if f.ContentRef != nil || f.Content == "" {
		return nil
	}
	ref, err := source.SaveText(ctx, f.Content)
	if err != nil {
		return err
	}
	f.Content, f.ContentRef = "", ref
	return nil
}

type PatchFile struct {
	BeforePath      string `json:",omitempty"`
	AfterPath       string `json:",omitempty"`
	Before          string `json:",omitempty"`
	BeforeRef       *Text  `json:",omitempty"`
	Exists          bool   `json:",omitzero"`
	Error           string `json:",omitempty"`
	TargetBefore    string `json:",omitempty"`
	TargetBeforeRef *Text  `json:",omitempty"`
	TargetExists    bool   `json:",omitzero"`
	TargetError     string `json:",omitempty"`
}

// Review keeps immutable path/status facts and direct counts alongside lazy text.
// Only Load returns a diffmodel value suitable for composition or painting.
type Review struct {
	diffmodel.ReviewFile
	Text    *Text `json:",omitempty"`
	Added   int   `json:",omitzero"`
	Removed int   `json:",omitzero"`
}

func Inline(file diffmodel.ReviewFile) Review { return Review{ReviewFile: file} }

func InlineReviews(files []diffmodel.ReviewFile) []Review {
	if files == nil {
		return nil
	}
	reviews := make([]Review, len(files))
	for i, file := range files {
		reviews[i] = Inline(file)
	}
	return reviews
}

// ReceiptFiles detaches bounded text, exact counts and per-file truncation flags.
// Receipts need no storage handle or later reads.
func ReceiptFiles(reviews []Review, limit int) ([]diffmodel.ReviewFile, [][2]int, []bool, error) {
	files := make([]diffmodel.ReviewFile, 0, len(reviews))
	counts := make([][2]int, 0, len(reviews))
	truncated := make([]bool, 0, len(reviews))
	for _, review := range reviews {
		file := review.ReviewFile
		if review.Text != nil {
			var err error
			file.Diff, err = review.Text.Prefix(limit)
			if err != nil {
				return nil, nil, nil, err
			}
		} else if len(file.Diff) > limit {
			file.Diff = file.Diff[:limit]
			for len(file.Diff) > 0 && !utf8.ValidString(file.Diff) {
				file.Diff = file.Diff[:len(file.Diff)-1]
			}
		}
		limit -= len(file.Diff)
		added, removed := review.LineCounts()
		counts = append(counts, [2]int{added, removed})
		truncated = append(truncated, len(file.Diff) < review.DiffBytes())
		files = append(files, file)
	}
	return files, counts, truncated, nil
}

func RetainReview(ctx context.Context, source *Source, file diffmodel.ReviewFile, budget *int) Review {
	r := Inline(file)
	if source == nil || len(file.Diff) <= InlineBytes && len(file.Diff) <= *budget {
		*budget -= len(file.Diff)
		return r
	}
	r.Added, r.Removed = file.LineCounts()
	ref, err := source.SaveText(ctx, file.Diff)
	if err != nil {
		file.Diff = ""
		file.Incomplete = "retain file evidence: " + err.Error()
		return Inline(file)
	}
	r.Diff, r.Text = "", ref
	return r
}

func (r Review) Load() (diffmodel.ReviewFile, error) {
	file := r.ReviewFile
	if r.Text != nil {
		var err error
		file.Diff, err = r.Text.Read()
		if err != nil {
			return file, err
		}
	}
	return file, nil
}

func (r Review) LineCounts() (int, int) {
	if r.Text != nil {
		return r.Added, r.Removed
	}
	return r.ReviewFile.LineCounts()
}

func (r Review) DiffBytes() int {
	if r.Text != nil {
		return r.Text.Size
	}
	return len(r.Diff)
}

// LoadReviews is a bounded projection, not the storage representation. Filtering
// occurs before text reads, so a narrow cold selection never expands its batch.
func LoadReviews(reviews []Review, keep func(diffmodel.ReviewFile) bool, limit int) ([]diffmodel.ReviewFile, error) {
	var files []diffmodel.ReviewFile
	for _, review := range reviews {
		if keep != nil && !keep(review.ReviewFile) {
			continue
		}
		if review.DiffBytes() > limit {
			return nil, errors.New("change projection exceeds its byte bound; narrow the selected paths")
		}
		file, err := review.Load()
		if err != nil {
			return nil, err
		}
		limit -= len(file.Diff)
		files = append(files, file)
	}
	return files, nil
}
