package sessionapi

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"slices"
	"strings"

	"github.com/yusing/mekugi/modules/livepreview/preview"
)

// Page budgets leave room for the RPC envelope and independent live state.
const pageEntries = 50
const feedBytes = 10 << 10
const entryBytes = 2 << 10

// Compact the presentation, not its raw transcript text. Disclosures retain
// their counts while their payload travels on the independent content channel.
// Anything removed from the inline view is acquired automatically by the reader.
func compactEntry(full entry) entry {
	p := full
	p.Values = nil
	p.Identities = nil
	identityBytes := 0
	for _, identity := range full.Identities {
		data, _ := json.Marshal(identity, encoding)
		if identityBytes+len(data) > 512 {
			p.AutoContent = true
			break
		}
		p.Identities = append(p.Identities, identity)
		identityBytes += len(data)
	}
	if len(p.Images) > 8 {
		p.Images = p.Images[:8]
		p.AutoContent = true
	}
	if len(p.Spans) > 32 {
		p.Spans = nil
		p.AutoContent = true
	}
	if len(full.Values) > 0 && !humanEntry(full.Agent) && full.Kind != "text" && full.Kind != "final" && full.Kind != "message" {
		p.Text = ""
	}
	if len(p.Text) > 512 {
		p.Text = strings.ToValidUTF8(p.Text[:512], "")
		p.Spans = nil
		p.AutoContent = true
	}
	if len(p.AssignmentText) > 512 {
		p.AssignmentText = ""
		p.AutoContent = true
	}
	used := 0
	for i := 0; i < len(full.Values); i++ {
		v := full.Values[i]
		if v.HiddenEdits > 0 {
			end := min(len(full.Values), i+v.HiddenEdits)
			v = value{Kind: v.Kind, Verb: v.Verb, Sources: v.Sources, Effect: v.Effect, HiddenEdits: end - i}
			i = end - 1
		} else {
			// These fields are dialog evidence, not transcript content.
			v.DetailCode = ""
			if len(v.Body) > 512 {
				v.Body = ""
				if !slices.Contains([]string{"op", "hook"}, v.Kind) {
					v.Body = strings.ToValidUTF8(full.Values[i].Body[:512], "")
					p.AutoContent = true
				}
			}
			if len(v.Details) > 512 {
				v.Details = ""
				p.AutoContent = true
			}
			if len(v.Summary) > 512 {
				v.Summary = strings.ToValidUTF8(v.Summary[:512], "")
				p.AutoContent = true
			}
			code := strings.TrimSuffix(v.Code, "\n")
			if len(code) > 512 {
				v.CodeLines = strings.Count(code, "\n") + 1
				if v.Lang == "json" && (v.CodeLines > 1 || v.Fenced && (v.Label != "" || len(v.Reads) > 0)) {
					var decoded any
					if json.Unmarshal([]byte(code), &decoded) == nil {
						v.CodeLines = jsonDisplayLines(decoded)
					}
				}
				v.Code = ""
				if v.Label == "" && len(v.Reads) == 0 {
					v.Code, _, _ = strings.Cut(code, "\n")
					if len(v.Code) > 512 {
						v.Code = ""
						p.AutoContent = true
					}
				}
				if !strings.Contains(code, "\n") && (v.Label == "" && len(v.Reads) == 0 || v.Fenced && v.Lang != "json") {
					p.AutoContent = true
				}
				if !strings.Contains(code, "\n") && (!v.Fenced || v.Label == "" && len(v.Reads) == 0) {
					v.CodeLines = 0
				}
			}
			tailBytes := 0
			for _, line := range v.Tail {
				tailBytes += len(line) + 1
			}
			if tailBytes > 512 && slices.ContainsFunc(v.Tail, func(line string) bool { return strings.TrimSpace(line) != "" }) {
				v.TailLines, v.Tail = len(v.Tail), nil
				if v.TailLines == 1 && v.TailOmitted == 0 || full.Kind == "hook" {
					p.AutoContent = true
				}
			}
		}
		data, _ := json.Marshal(v, encoding)
		if len(data) > 4096 || used+len(data) > 7000 {
			// Unbounded labels, paths and structured inline lists must also stay
			// off the navigation socket. Never cut through Markdown or JSON.
			p.AutoContent = true
			break
		}
		p.Values = append(p.Values, v)
		used += len(data)
	}
	return p
}

// Match the JSON disclosure's expansion of embedded JSON strings without
// shipping or formatting their payload just to retain the disclosure count.
func jsonDisplayLines(value any) int {
	switch value := value.(type) {
	case string:
		text := strings.TrimSpace(value)
		if strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[") {
			var nested any
			if json.Unmarshal([]byte(text), &nested) == nil {
				return jsonDisplayLines(nested)
			}
		}
	case []any:
		if len(value) == 0 {
			return 1
		}
		lines := 2
		for _, item := range value {
			lines += jsonDisplayLines(item)
		}
		return lines
	case map[string]any:
		if len(value) == 0 {
			return 1
		}
		lines := 2
		for _, item := range value {
			lines += jsonDisplayLines(item)
		}
		return lines
	}
	return 1
}

type PageCursor struct {
	Attachment string `json:"attachment"`
	Kind       string `json:"kind"`
	Revision   uint64 `json:"revision"`
	Before     uint64 `json:"before,omitzero"`
	Caller     string `json:"caller,omitzero"`
}

type ContentReference struct {
	Key        string `json:"key,omitzero"`
	Caller     string `json:"caller,omitzero"`
	Thread     string `json:"thread,omitzero"`
	Attachment string `json:"attachment"`
	Kind       string `json:"kind"`
	Revision   uint64 `json:"revision"`
	Seq        uint64 `json:"seq,omitzero"`
}

type PageResult struct {
	Roster   *roster      `json:"roster,omitzero"`
	Delivery string       `json:"delivery,omitzero"`
	Files    []reviewFile `json:"files,omitzero"`
	Next     *PageCursor  `json:"next,omitzero"`
	Entries  []entry      `json:"entries"`
	Earlier  *PageCursor  `json:"earlier"`
}

type ContentResult struct {
	Session    *Session          `json:"session,omitzero"`
	Input      *Input            `json:"input,omitzero"`
	Prompts    *Prompts          `json:"prompts,omitzero"`
	Status     *status           `json:"status,omitzero"`
	ReviewInfo *savedReview      `json:"reviewInfo,omitzero"`
	Live       []preview.Preview `json:"live,omitzero"`
	Agent      *agent            `json:"agent,omitzero"`
	Review     *reviewFile       `json:"review,omitzero"`
	Entry      *entry            `json:"entry,omitzero"`
	Details    *NativeDetails    `json:"details,omitzero"`
}

// feedPage borrows the retained projection. Only the requested window is
// copied; a large entry keeps its complete public projection behind a reference.
func (s *Server) feedPage(kind string, f *feed, before uint64, caller string) (*feedPatch, error) {
	p := &feedPatch{Attachment: s.readAttachment, Order: []uint64{}, Revision: f.history}
	used := 0
	end := len(f.order)
	if before != 0 {
		end = f.entries[before].position
	}
	for i := end - 1; i >= 0; i-- {
		seq := f.order[i]
		stored := f.entries[seq]
		if caller != "" && stored.agent != caller {
			continue
		}
		data := stored.data
		if stored.brief != nil {
			preview := *stored.brief
			preview.Content = &ContentReference{Attachment: s.readAttachment, Kind: kind, Revision: stored.revision, Seq: seq}
			var err error
			data, err = json.Marshal(preview, encoding)
			if err != nil {
				return nil, err
			}
		}
		if len(p.Order) == pageEntries || used+len(data)+24 > feedBytes {
			if len(p.Order) == 0 {
				return nil, fmt.Errorf("entry identity exceeds the page budget")
			}
			p.Earlier = &PageCursor{Attachment: s.readAttachment, Kind: kind, Revision: f.history, Before: p.Order[len(p.Order)-1], Caller: caller}
			break
		}
		p.Order = append(p.Order, seq)
		p.Entries = append(p.Entries, data)
		used += len(data) + 24
	}
	slices.Reverse(p.Order)
	slices.Reverse(p.Entries)
	if before == 0 && caller == "" && len(p.Order) > 0 {
		f.window = uint64(f.entries[p.Order[0]].position)
	}
	if before == 0 && caller == "" {
		f.windowBytes = used + 256
	}
	return p, nil
}

func (s *Server) page(body jsontext.Value) (any, *RPCError) {
	var cursor PageCursor
	if failure := decodeBody(body, &cursor); failure != nil {
		return nil, failure
	}
	s.mu.Lock()
	if cursor.Kind == "roster" {
		value, attachment, revision := s.rosterValue, s.readAttachment, s.rosterRevision
		s.mu.Unlock()
		return rosterPage(value, attachment, revision, cursor)
	}
	if cursor.Kind == "review" {
		view, attachment := s.reviewSource, s.readAttachment
		s.mu.Unlock()
		return reviewPage(view, attachment, cursor)
	}
	defer s.mu.Unlock()
	f := s.readFeed(cursor.Kind)
	if f == nil || cursor.Attachment != s.readAttachment || cursor.Revision != f.history {
		return nil, rpcFailure(409, "History changed; read its latest publication before paging.")
	}
	if _, ok := f.entries[cursor.Before]; cursor.Before != 0 && !ok {
		return nil, rpcFailure(409, "The history boundary is no longer available.")
	}
	p, err := s.feedPage(cursor.Kind, f, cursor.Before, cursor.Caller)
	if err != nil {
		return nil, rpcFailure(500, "History page unavailable.")
	}
	result := PageResult{Entries: []entry{}, Earlier: p.Earlier}
	for _, data := range p.Entries {
		var value entry
		if err := json.Unmarshal(data, &value); err != nil {
			return nil, rpcFailure(500, "History entry unavailable.")
		}
		result.Entries = append(result.Entries, value)
	}
	return result, nil
}

func (s *Server) content(body jsontext.Value) (any, *RPCError) {
	var ref ContentReference
	if failure := decodeBody(body, &ref); failure != nil {
		return nil, failure
	}
	if ref.Kind == "details" {
		return s.Native().readDetails(ref)
	}
	if ref.Kind == "session" || ref.Kind == "input" || ref.Kind == "prompts" || ref.Kind == "status" || ref.Kind == "review-info" || ref.Kind == "live" {
		return s.sectionContent(ref)
	}
	s.mu.Lock()
	if ref.Kind == "roster" {
		value := s.rosterValue
		if value == nil || ref.Attachment != s.readAttachment || ref.Revision != s.rosterRevision || ref.Seq == 0 || ref.Seq > uint64(len(value.Agents)) {
			s.mu.Unlock()
			return nil, rpcFailure(409, "The roster changed; read its current publication.")
		}
		full := value.Agents[ref.Seq-1]
		s.mu.Unlock()
		return ContentResult{Agent: &full}, nil
	}
	if ref.Kind == "review" {
		view, attachment := s.reviewSource, s.readAttachment
		s.mu.Unlock()
		return reviewContent(view, attachment, ref)
	}
	f := s.readFeed(ref.Kind)
	if f == nil || ref.Attachment != s.readAttachment {
		s.mu.Unlock()
		return nil, rpcFailure(409, "The content attachment changed.")
	}
	stored, ok := f.entries[ref.Seq]
	if !ok || stored.revision != ref.Revision {
		s.mu.Unlock()
		return nil, rpcFailure(409, "The referenced entry changed.")
	}
	s.mu.Unlock()
	var full entry
	if err := json.Unmarshal(stored.data, &full); err != nil {
		return nil, rpcFailure(500, "Entry content unavailable.")
	}
	return ContentResult{Entry: &full}, nil
}

func rosterPage(value *roster, attachment string, revision uint64, cursor PageCursor) (PageResult, *RPCError) {
	if value == nil || cursor.Attachment != attachment || cursor.Revision != revision || cursor.Before > uint64(len(value.Agents)) {
		return PageResult{}, rpcFailure(409, "The roster changed; read its current publication.")
	}
	page := roster{Agents: []agent{}, Added: value.Added, Removed: value.Removed, Total: len(value.Agents)}
	used := 0
	for index := int(cursor.Before); index < len(value.Agents); index++ {
		agent := value.Agents[index]
		if cursor.Caller != "" && !slices.Contains(agent.Aliases, cursor.Caller) {
			continue
		}
		data, err := json.Marshal(agent, encoding)
		if err != nil {
			return PageResult{}, rpcFailure(500, "Agent metadata unavailable.")
		}
		if len(data) > 1024 {
			agent.Activity, agent.Wait, agent.Skills, agent.Items = nil, nil, nil, nil
			agent.Identities = nil
			agent.Content = &ContentReference{Attachment: attachment, Kind: "roster", Revision: revision, Seq: uint64(index + 1)}
			data, err = json.Marshal(agent, encoding)
			if err != nil {
				return PageResult{}, rpcFailure(500, "Agent metadata unavailable.")
			}
		}
		if len(page.Agents) == pageEntries || used+len(data) > 4<<10 {
			if len(page.Agents) == 0 {
				return PageResult{}, rpcFailure(413, "Agent identity exceeds the page budget.")
			}
			cursor.Before = uint64(index)
			page.Next = &cursor
			break
		}
		page.Agents = append(page.Agents, agent)
		used += len(data)
	}
	return PageResult{Roster: &page}, nil
}

func (s *Server) readFeed(kind string) *feed {
	switch kind {
	case "main":
		return &s.main
	case "agents":
		return &s.agents
	default:
		return nil
	}
}
