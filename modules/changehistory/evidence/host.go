package evidence

import (
	json "encoding/json/v2"
	"errors"

	"github.com/yusing/mekugi/contracts/activitydetail"

	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

// Root selects this owner's attachment, without interpreting other owners.
func Root(record history.HostRecord) (string, error) {
	root := ""
	for _, name := range record.Attachments {
		if !IsManifest(name) {
			continue
		}
		if root != "" {
			return "", errors.New("multiple capture evidence attachments")
		}
		root = name
	}
	if root != "" && (len(record.History.ReviewFiles) > 0 || len(record.History.NativePatches) > 0 || record.History.ExecObservation != nil || record.History.ResolvedBaseline != nil) {
		return "", errors.New("conflicting inline and attached capture evidence")
	}
	return root, nil
}

// ObservationMode projects the mode from already-admitted manifest metadata.
func (m Manifest) ObservationMode() (present, codeMode bool, err error) {
	if len(m.Observation) == 0 {
		return false, false, nil
	}
	var mode struct{ CodeMode bool }
	err = json.Unmarshal(m.Observation, &mode)
	return true, mode.CodeMode, err
}

// Anchor reads only the persisted source boundary, never current workspace files.
func Anchor(source *Source, record history.HostRecord) (activitydetail.SourceAnchor, error) {
	root, err := Root(record)
	if err != nil || root == "" {
		return activitydetail.SourceAnchor{}, err
	}
	manifest, err := Open(source, root)
	return manifest.SourceAnchor, err
}

// Reviews reads only review metadata, not baselines or review text. Historical
// records arrive already validated by their frozen codec and stay readable.
func Reviews(source *Source, record history.HostRecord) ([]Review, error) {
	root, err := Root(record)
	if err != nil {
		return nil, err
	}
	if root == "" {
		files := make([]Review, len(record.History.ReviewFiles))
		for i, file := range record.History.ReviewFiles {
			files[i] = Inline(diffmodel.ReviewFile(file))
		}
		return files, nil
	}
	manifest, err := Open(source, root)
	if err != nil {
		return nil, err
	}
	return ReadReviews(source, manifest.Reviews)
}
