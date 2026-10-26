package sessionapi

import (
	"bytes"
	"encoding/json/v2"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
)

// deferDetails retains only the admitted view's already-public projection.
// Selection revisions and output identities skip unchanged full serialization.
func (v *nativeView) deferDetails(id string, frame *NativeFrame) error {
	details := frame.Details
	if details == nil {
		v.details, v.detailsKey = nil, nil
		return nil
	}
	key := NativeDetails{Source: details.Source, Selected: details.Selected, Agent: details.Agent,
		View: sessionpresentation.DetailView{ID: details.View.ID, Revision: details.View.Revision}}
	if details.Output != nil {
		key.Output = &sessionpresentation.DetailOutput{DetailOutputRequest: details.Output.DetailOutputRequest}
	}
	encodedKey, err := json.Marshal(struct {
		Thread     string
		Attachment uint64
		Details    NativeDetails
	}{frame.View.Main.ID, frame.View.Version.Attachment, key}, nativeEncoding)
	if err != nil {
		return err
	}
	if details.View.Revision == 0 || !bytes.Equal(encodedKey, v.detailsKey) {
		data, err := json.Marshal(details, nativeEncoding)
		if err != nil {
			return err
		}
		if !bytes.Equal(data, v.details) || !bytes.Equal(encodedKey, v.detailsKey) {
			v.detailsRevision++
		}
		v.detailsKey, v.details = encodedKey, data
	}
	if len(v.details) <= entryBytes {
		return nil
	}
	key.Output = nil
	key.View.Pages = []sessionpresentation.DetailItem{}
	key.Content = &ContentReference{Thread: frame.View.Main.ID, Attachment: id, Kind: "details", Revision: v.detailsRevision}
	frame.Details = &key
	return nil
}

func (n *Native) readDetails(ref ContentReference) (any, *RPCError) {
	n.mu.Lock()
	v := n.views[ref.Attachment]
	if v == nil || v.ctx.Err() != nil || v.authority != remoteHumanView || v.target.thread != ref.Thread || v.detailsRevision != ref.Revision || len(v.details) == 0 {
		n.mu.Unlock()
		return nil, rpcFailure(409, "The detail selection changed or its view closed.")
	}
	data := v.details
	n.mu.Unlock()
	var details NativeDetails
	if err := json.Unmarshal(data, &details, nativeEncoding); err != nil {
		return nil, rpcFailure(500, "Detail content unavailable.")
	}
	return ContentResult{Details: &details}, nil
}
