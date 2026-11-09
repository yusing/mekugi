package evidence

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/yusing/mekugi/contracts/recordkind"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

const inlineBudget = 8 << 20

var manifestName = recordkind.HashedName("change-evidence-", ".json")
var fileName = recordkind.HashedName("change-file-", ".json")

// Snapshot is the feature-owned acquisition/review value. Observation and
// Baseline contain their owner's metadata only; file collections are separate.
type Snapshot struct {
	Observation   jsontext.Value
	Files         []File
	Baseline      jsontext.Value
	BaselineFiles []File
	Patches       []Patch
	Reviews       []Review
}

type Patch struct {
	Input string
	Files []PatchFile
}

type patchRefs struct {
	Input string
	Files []string
}

// Manifest text is bounded metadata. It never expands the referenced texts.
type Manifest struct {
	Version       int
	Observation   jsontext.Value `json:",omitempty"`
	Files         []string       `json:",omitempty"`
	Baseline      jsontext.Value `json:",omitempty"`
	BaselineFiles []string       `json:",omitempty"`
	Patches       []patchRefs    `json:",omitempty"`
	Reviews       []string       `json:",omitempty"`
}

type fileRecord struct {
	Version int
	File    *File      `json:",omitempty"`
	Patch   *PatchFile `json:",omitempty"`
	Review  *Review    `json:",omitempty"`
}

func (r fileRecord) texts() []*Text {
	var texts []*Text
	if r.File != nil {
		texts = append(texts, r.File.ContentRef)
	}
	if r.Patch != nil {
		texts = append(texts, r.Patch.BeforeRef, r.Patch.TargetBeforeRef)
	}
	if r.Review != nil {
		texts = append(texts, r.Review.Text)
	}
	return texts
}

func (r fileRecord) validate(source *Source) error {
	kinds := 0
	if r.File != nil {
		kinds++
		if len(r.File.Content) > InlineBytes || r.File.Content != "" && r.File.ContentRef != nil {
			return errors.New("conflicting endpoint text")
		}
	}
	if r.Patch != nil {
		kinds++
		if len(r.Patch.Before) > InlineBytes || len(r.Patch.TargetBefore) > InlineBytes || r.Patch.Before != "" && r.Patch.BeforeRef != nil || r.Patch.TargetBefore != "" && r.Patch.TargetBeforeRef != nil {
			return errors.New("conflicting patch text")
		}
	}
	if r.Review != nil {
		kinds++
		if len(r.Review.Diff) > InlineBytes || r.Review.Diff != "" && r.Review.Text != nil || r.Review.Added < -1 || r.Review.Removed < -1 {
			return errors.New("conflicting review text")
		}
	}
	if r.Version != 1 || kinds != 1 {
		return errors.New("invalid file evidence kind/version")
	}
	for _, text := range r.texts() {
		if text == nil {
			continue
		}
		// Dependency inspection has no reader authority. Validate using a
		// detached handle, then attach only for an admitted read.
		if err := text.Bind(source); err != nil {
			return err
		}
	}
	return nil
}

func (m Manifest) dependencies() ([]string, error) {
	if m.Version != 1 {
		return nil, errors.New("invalid capture manifest version")
	}
	refs := append(append(append([]string{}, m.Files...), m.BaselineFiles...), m.Reviews...)
	for _, patch := range m.Patches {
		refs = append(refs, patch.Files...)
	}
	for _, ref := range refs {
		if !fileName(ref) {
			return nil, errors.New("invalid capture file dependency")
		}
	}
	return refs, nil
}

func RecordKinds() []recordkind.Spec {
	return []recordkind.Spec{
		{ID: "change-stage", Match: stagedName, MaxRecordBytes: ChunkBytes, Dependencies: func(record recordkind.Record) ([]string, error) {
			var staged stagedText
			if err := decode(record.Name, record.Data, "change-stage-", &staged); err != nil {
				return nil, err
			}
			if err := staged.Text.Bind(nil); err != nil {
				return nil, err
			}
			return staged.Text.Parts, nil
		}},
		{ID: "change-evidence", Match: manifestName, MaxRecordBytes: history.MaxRecordBytes, Dependencies: func(record recordkind.Record) ([]string, error) {
			var m Manifest
			if err := decode(record.Name, record.Data, "change-evidence-", &m); err != nil {
				return nil, err
			}
			return m.dependencies()
		}},
		{ID: "change-file", Match: fileName, MaxRecordBytes: ChunkBytes, Dependencies: func(record recordkind.Record) ([]string, error) {
			var r fileRecord
			if err := decode(record.Name, record.Data, "change-file-", &r); err != nil {
				return nil, err
			}
			if err := r.validate(nil); err != nil {
				return nil, err
			}
			var refs []string
			for _, text := range r.texts() {
				if text != nil {
					refs = append(refs, text.Parts...)
				}
			}
			return refs, nil
		}},
		{ID: "change-text", Match: textName, MaxRecordBytes: ChunkBytes},
	}
}

func decode(name string, data []byte, prefix string, value any) error {
	if prefix+digest(data)+".json" != name {
		return fmt.Errorf("invalid retained evidence digest: %s", name)
	}
	return json.Unmarshal(data, value, json.RejectUnknownMembers(true))
}

// Write publishes bounded file records before their referencing manifest. Text
// already spilled during acquisition is reused without loading or re-encoding.
func Write(tx *history.Transaction, source *Source, snapshot Snapshot) (string, error) {
	budget := inlineBudget
	type pending struct {
		name string
		data []byte
	}
	var pendingFiles []pending
	var textRefs, fileRefs []string
	metadataBytes := 0
	spill := func(value *string, ref **Text) error {
		if *ref != nil {
			bound := **ref
			if err := bound.Bind(source); err != nil {
				return err
			}
			*ref = &bound
			return nil
		}
		if len(*value) <= InlineBytes && len(*value) <= budget {
			budget -= len(*value)
			return nil
		}
		text, err := source.saveText(tx, *value)
		if err != nil {
			return err
		}
		*value, *ref = "", text
		return nil
	}
	write := func(prefix string, value any, limit int) (string, error) {
		data, err := json.Marshal(value, json.Deterministic(true))
		if err != nil {
			return "", err
		}
		if len(data) > limit {
			return "", errors.New("file evidence metadata exceeds its record bound")
		}
		name := prefix + digest(data) + ".json"
		if record, ok := value.(fileRecord); ok {
			metadataBytes += len(data)
			if metadataBytes > history.MaxRecordBytes {
				return "", errors.New("capture file metadata exceeds its bound")
			}
			for _, text := range record.texts() {
				if text != nil {
					textRefs = append(textRefs, text.Parts...)
				}
			}
			pendingFiles = append(pendingFiles, pending{name, data})
			fileRefs = append(fileRefs, name)
			return name, nil
		}
		if err := source.put(tx, name, data); err != nil {
			return "", err
		}
		return name, nil
	}
	files := func(values []File) ([]string, error) {
		var refs []string
		for _, file := range values {
			if err := spill(&file.Content, &file.ContentRef); err != nil {
				return nil, err
			}
			name, err := write("change-file-", fileRecord{Version: 1, File: &file}, ChunkBytes)
			if err != nil {
				return nil, err
			}
			refs = append(refs, name)
		}
		return refs, nil
	}
	m := Manifest{Version: 1, Observation: snapshot.Observation, Baseline: snapshot.Baseline}
	var err error
	if m.Files, err = files(snapshot.Files); err != nil {
		return "", err
	}
	if m.BaselineFiles, err = files(snapshot.BaselineFiles); err != nil {
		return "", err
	}
	for _, patch := range snapshot.Patches {
		refs := patchRefs{Input: patch.Input}
		for _, file := range patch.Files {
			if err := spill(&file.Before, &file.BeforeRef); err != nil {
				return "", err
			}
			if err := spill(&file.TargetBefore, &file.TargetBeforeRef); err != nil {
				return "", err
			}
			name, err := write("change-file-", fileRecord{Version: 1, Patch: &file}, ChunkBytes)
			if err != nil {
				return "", err
			}
			refs.Files = append(refs.Files, name)
		}
		m.Patches = append(m.Patches, refs)
	}
	for _, review := range snapshot.Reviews {
		if review.Text == nil {
			review.Added, review.Removed = review.ReviewFile.LineCounts()
		}
		if err := spill(&review.Diff, &review.Text); err != nil {
			return "", err
		}
		name, err := write("change-file-", fileRecord{Version: 1, Review: &review}, ChunkBytes)
		if err != nil {
			return "", err
		}
		m.Reviews = append(m.Reviews, name)
	}
	// Batch ownership publication, rather than rewriting the growing catalog
	// once per captured file. Each layer is owned before its referrers appear.
	checked := make(map[string]bool, len(textRefs))
	for _, name := range textRefs {
		if checked[name] {
			continue
		}
		checked[name] = true
		data, err := source.read(name, ChunkBytes)
		if err != nil {
			return "", err
		}
		if "change-text-"+digest(data)+".bin" != name {
			return "", fmt.Errorf("invalid retained text part: %s", name)
		}
	}
	if err := tx.Retain(source.Thread, source.Limits, textRefs...); err != nil {
		return "", err
	}
	for _, file := range pendingFiles {
		if err := source.put(tx, file.name, file.data); err != nil {
			return "", err
		}
	}
	if err := tx.Retain(source.Thread, source.Limits, fileRefs...); err != nil {
		return "", err
	}
	name, err := write("change-evidence-", &m, history.MaxRecordBytes)
	if err != nil {
		return "", err
	}
	if err := tx.Retain(source.Thread, source.Limits, name); err != nil {
		return "", err
	}
	return name, nil
}

func Open(source *Source, name string) (Manifest, error) {
	var m Manifest
	if !manifestName(name) {
		return m, errors.New("invalid capture evidence reference/reader")
	}
	data, err := source.read(name, history.MaxRecordBytes)
	if err != nil {
		return m, err
	}
	if err := decode(name, data, "change-evidence-", &m); err != nil {
		return m, err
	}
	_, err = m.dependencies()
	return m, err
}

func IsManifest(name string) bool { return manifestName(name) }

func readFile(source *Source, name string, budget *int) (fileRecord, error) {
	var r fileRecord
	if !fileName(name) {
		return r, errors.New("invalid file evidence reference")
	}
	data, err := source.read(name, ChunkBytes)
	if err != nil {
		return r, err
	}
	if len(data) > ChunkBytes {
		return r, errors.New("file evidence exceeds its metadata bound")
	}
	*budget -= len(data)
	if *budget < 0 {
		return r, errors.New("capture file metadata exceeds its bound")
	}
	if err := decode(name, data, "change-file-", &r); err != nil {
		return r, err
	}
	return r, r.validate(source)
}

func ReadFiles(source *Source, refs []string) ([]File, error) {
	budget := history.MaxRecordBytes
	return readFiles(source, refs, &budget)
}

func readFiles(source *Source, refs []string, budget *int) ([]File, error) {
	var files []File
	for _, ref := range refs {
		r, err := readFile(source, ref, budget)
		if err != nil {
			return nil, err
		}
		if r.File == nil {
			return nil, errors.New("expected endpoint evidence")
		}
		files = append(files, *r.File)
	}
	return files, nil
}

func ReadReviews(source *Source, refs []string) ([]Review, error) {
	budget := history.MaxRecordBytes
	return readReviews(source, refs, &budget)
}

func readReviews(source *Source, refs []string, budget *int) ([]Review, error) {
	var files []Review
	for _, ref := range refs {
		r, err := readFile(source, ref, budget)
		if err != nil {
			return nil, err
		}
		if r.Review == nil {
			return nil, errors.New("expected review evidence")
		}
		files = append(files, *r.Review)
	}
	return files, nil
}

func Read(source *Source, name string) (Snapshot, error) {
	var result Snapshot
	m, err := Open(source, name)
	if err != nil {
		return result, err
	}
	result.Observation, result.Baseline = m.Observation, m.Baseline
	budget := history.MaxRecordBytes
	if result.Files, err = readFiles(source, m.Files, &budget); err != nil {
		return result, err
	}
	if result.BaselineFiles, err = readFiles(source, m.BaselineFiles, &budget); err != nil {
		return result, err
	}
	if result.Reviews, err = readReviews(source, m.Reviews, &budget); err != nil {
		return result, err
	}
	for _, patch := range m.Patches {
		value := Patch{Input: patch.Input}
		for _, ref := range patch.Files {
			r, err := readFile(source, ref, &budget)
			if err != nil {
				return result, err
			}
			if r.Patch == nil {
				return result, errors.New("expected patch evidence")
			}
			value.Files = append(value.Files, *r.Patch)
		}
		result.Patches = append(result.Patches, value)
	}
	return result, nil
}
