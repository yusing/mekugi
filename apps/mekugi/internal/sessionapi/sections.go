package sessionapi

import (
	"cmp"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"slices"
	"strings"
)

type DeferredSections struct {
	Session *ContentReference `json:"session,omitzero"`
	Input   *ContentReference `json:"input,omitzero"`
	Prompts *ContentReference `json:"prompts,omitzero"`
	Status  *ContentReference `json:"status,omitzero"`
	Review  *ContentReference `json:"review,omitzero"`
	Live    *ContentReference `json:"live,omitzero"`
}

// encodeShared leaves RPC envelope room after two feed windows and a roster
// window. Auxiliary content has explicit references instead of silent loss.
func (s *Server) encodeShared(kind string, m message) (publication, error) {
	refs := &DeferredSections{}
	ref := func(kind string) *ContentReference {
		return &ContentReference{Attachment: s.readAttachment, Kind: kind, Revision: s.contentVersions[kind]}
	}
	short := func(text string) string { return strings.ToValidUTF8(text[:min(len(text), 256)], "") }
	session, err := json.Marshal(s.session, encoding)
	if err != nil {
		return publication{}, err
	}
	input, err := json.Marshal(s.input, encoding)
	if err != nil {
		return publication{}, err
	}
	type sectionSize struct {
		kind          string
		size, preview int
	}
	sizes := []sectionSize{{"session", len(session), 1400}, {"input", len(input), 600}, {"prompts", len(s.prompts), 250}, {"status", len(s.status), 600}, {"review-info", len(s.review), 1300}, {"live", len(s.livePreviews), 250}}
	total := s.main.windowBytes + s.agents.windowBytes + len(s.roster) + 512
	for _, section := range sizes {
		total += section.size
	}
	slices.SortFunc(sizes, func(a, b sectionSize) int { return cmp.Compare(b.size-b.preview, a.size-a.preview) })
	deferred := map[string]bool{}
	for _, section := range sizes {
		if total <= 30<<10 {
			break
		}
		if section.size <= section.preview {
			continue
		}
		deferred[section.kind] = true
		total -= section.size - section.preview
	}
	// If a smaller latest window makes an auxiliary section fit again, deliver
	// it even when the auxiliary owner itself did not change this publication.
	for kind := range s.sharedDeferred {
		if deferred[kind] {
			continue
		}
		switch kind {
		case "session":
			m.Session = &s.session
		case "input":
			m.Input = s.input
		case "prompts":
			m.Prompts = s.prompts
		case "status":
			m.Status = s.status
		case "review-info":
			m.Review = s.review
		case "live":
			m.Live = s.livePreviews
		}
	}
	s.sharedDeferred = deferred
	if deferred["session"] {
		refs.Session = ref("session")
		if m.Session != nil {
			value := *m.Session
			value.Title, value.Workspace, value.Caller, value.Role = short(value.Title), short(value.Workspace), short(value.Caller), short(value.Role)
			m.Session = &value
		}
	}
	if s.input != nil && deferred["input"] {
		refs.Input = ref("input")
		if m.Input != nil {
			value := *m.Input
			value.Reason = short(value.Reason)
			m.Input = &value
		}
	}
	if deferred["prompts"] {
		refs.Prompts = ref("prompts")
		if m.Prompts != nil {
			m.Prompts = jsontext.Value(`{"questions":[],"approvals":[]}`)
		}
	}
	if deferred["status"] {
		refs.Status = ref("status")
		if m.Status != nil {
			var value status
			if err := json.Unmarshal(s.status, &value); err != nil {
				return publication{}, err
			}
			value.Text = short(value.Text)
			value.Failure = short(value.Failure)
			m.Status, err = json.Marshal(value, encoding)
			if err != nil {
				return publication{}, err
			}
		}
	}
	if deferred["review-info"] {
		refs.Review = ref("review-info")
		if m.Review != nil && string(m.Review) != "null" {
			var value savedReview
			if err := json.Unmarshal(s.review, &value); err != nil {
				return publication{}, err
			}
			value.Callers, value.Counts.Callers = nil, nil
			value.Workspace, value.Coverage, value.Failure = short(value.Workspace), short(value.Coverage), short(value.Failure)
			m.Review, err = json.Marshal(value, encoding)
			if err != nil {
				return publication{}, err
			}
		}
	}
	if deferred["live"] {
		refs.Live = ref("live")
		if m.Live != nil {
			m.Live = jsontext.Value(`[]`)
		}
	}
	m.Deferred = refs
	event, err := encodeEvent(kind, &m)
	if err == nil && len(event.Data) > 30<<10 {
		err = fmt.Errorf("shared publication identity exceeds its delivery budget")
	}
	return event, err
}

func (s *Server) sectionContent(ref ContentReference) (any, *RPCError) {
	s.mu.Lock()
	if ref.Attachment != s.readAttachment || ref.Revision != s.contentVersions[ref.Kind] {
		s.mu.Unlock()
		return nil, rpcFailure(409, "The referenced section changed.")
	}
	result := ContentResult{}
	var data jsontext.Value
	var target any
	switch ref.Kind {
	case "session":
		result.Session = new(s.session)
	case "input":
		if s.input != nil {
			result.Input = new(*s.input)
		}
	case "prompts":
		data, target = s.prompts, &result.Prompts
	case "status":
		data, target = s.status, &result.Status
	case "review-info":
		data, target = s.review, &result.ReviewInfo
	case "live":
		data, target = s.livePreviews, &result.Live
	}
	s.mu.Unlock()
	if target != nil && json.Unmarshal(data, target) != nil {
		return nil, rpcFailure(500, "Section content unavailable.")
	}
	return result, nil
}
