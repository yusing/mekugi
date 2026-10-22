// Package compact owns immutable model summaries and separate reload selections.
package compact

import (
	"crypto/sha256"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"strings"

	"github.com/yusing/mekugi/contracts/recordkind"
)

const Prefix = "mekugi:compact:"

func Name(reference string) string {
	return fmt.Sprintf("compact-%x.json", sha256.Sum256([]byte(reference)))
}

var Kind = recordkind.Spec{ID: "compact", Match: func(name string) bool {
	hash := strings.TrimSuffix(strings.TrimPrefix(name, "compact-"), ".json")
	return len(hash) == 64 && strings.Trim(hash, "0123456789abcdef") == "" && name == "compact-"+hash+".json"
}}

type Record struct {
	Version            int
	Reference, Summary string
	Selection          string
}

func Decode(data []byte, reference string) (Record, error) {
	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		return r, err
	}
	if r.Version != 1 || r.Reference != reference || strings.TrimSpace(r.Summary) == "" {
		return r, errors.New("invalid retained compaction summary")
	}
	_, err := ReadCommands(r.Selection)
	return r, err
}
