package router

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
)

type inspectedJournalNode struct {
	Path         string                   `json:"path"`
	Kind         string                   `json:"kind"`
	State        string                   `json:"state,omitempty"`
	Author       string                   `json:"author"`
	Agent        string                   `json:"agent,omitempty"`
	SupersededBy string                   `json:"superseded_by,omitempty"`
	Created      journalStamp             `json:"created"`
	Updated      journalStamp             `json:"updated"`
	Text         map[string]inspectedText `json:"text"`
}

type sessionJournalInspection struct {
	Schema       string                 `json:"schema"`
	ThreadID     string                 `json:"thread_id"`
	Workspace    string                 `json:"workspace,omitempty"`
	Availability string                 `json:"availability"`
	TotalNodes   *int                   `json:"total_nodes,omitempty"`
	Offset       int                    `json:"offset"`
	NextOffset   *int                   `json:"next_offset,omitempty"`
	Nodes        []inspectedJournalNode `json:"nodes"`
}

func inspectSessionJournal(ctx context.Context, store *mekugiReplayStore, metadata sessionAXInput, override, field string, offset, limit, textBytes int, stdout io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if metadata.ThreadID == "" {
		return errors.New("journal inspection requires session_meta.id")
	}
	workspace := override
	if workspace == "" {
		var err error
		workspace, err = canonicalInspectionWorkspace(metadata.Cwd)
		if err != nil {
			return err
		}
	}
	result := sessionJournalInspection{
		Schema: "mekugi.session.journal.v1", ThreadID: metadata.ThreadID, Workspace: workspace,
		Availability: "workspace_unavailable", Offset: offset, Nodes: []inspectedJournalNode{},
	}
	if workspace != "" {
		journal, exists, err := readThreadJournal(store, workspace, metadata.ThreadID)
		if err != nil {
			return fmt.Errorf("journal for thread %q: %w", metadata.ThreadID, err)
		}
		result.Availability = "missing"
		if exists {
			result.Availability = "matched"
			result.TotalNodes = new(len(journal.Items))
			start := min(offset, len(journal.Items))
			end := min(start+limit, len(journal.Items))
			for _, item := range journal.Items[start:end] {
				if err := ctx.Err(); err != nil {
					return err
				}
				node := item.node()
				row := inspectedJournalNode{
					Path: node.Path, Kind: node.Kind, State: node.State, Author: node.Author,
					Agent: node.Agent, SupersededBy: node.SupersededBy, Created: node.Created, Updated: node.Updated,
					Text: make(map[string]inspectedText),
				}
				for name, value := range map[string]string{"title": node.Title, "body": node.Body, "reason": node.Reason, "question": node.Question} {
					row.Text[name] = inspectText(value, field == "all" || field == name, textBytes)
				}
				result.Nodes = append(result.Nodes, row)
			}
			if end < len(journal.Items) {
				result.NextOffset = new(end)
			}
		}
	}
	return json.MarshalEncode(jsontext.NewEncoder(stdout), &result)
}
