package binding

import (
	"errors"

	"github.com/yusing/mekugi/contracts/guidance"
)

// MatchInstructionFragment binds provider content to a previously admitted host
// fragment even when provider serialization omits the native metadata.
func MatchInstructionFragment(fragment guidance.Fragment, thread, item string, index int, text string) error {
	if thread == "" || item == "" || fragment.Thread != thread || fragment.Item != item || fragment.Index != index {
		return errors.New("instruction provenance identity mismatch")
	}
	if fragment.Text != text {
		return errors.New("instruction provenance content mismatch")
	}
	return nil
}
