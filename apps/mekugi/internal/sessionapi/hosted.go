package sessionapi

import (
	"context"
	"errors"
	"slices"
)

// Launcher constructs existing execution owners inside the server process.
// Run blocks until the owned runtime and its resources have stopped.
type Launcher interface {
	StartOptions(context.Context) (StartOptions, error)
	PrepareInput(context.Context, string, InputRequest) (*PreparedInput, error)
	Run(context.Context, Start, *Server) error
}

type StartOptions struct {
	Workspaces   []string     `json:"workspaces"`
	Models       []ModelGroup `json:"models"`
	DefaultModel string       `json:"defaultModel"`
	Note         string       `json:"note,omitzero"`
}
type ModelGroup struct {
	Label  string  `json:"label"`
	Models []Model `json:"models"`
}
type Model struct {
	ID            string   `json:"id"`
	Label         string   `json:"label"`
	Efforts       []string `json:"efforts"`
	DefaultEffort string   `json:"defaultEffort,omitzero"`
}
type Start struct {
	Native      *NativeStart `json:"-"`
	Replay      *ReplayStart `json:"replay,omitzero"`
	Workspace   string       `json:"workspace"`
	Model       string       `json:"model"`
	Effort      string       `json:"effort,omitzero"`
	Text        string       `json:"text"`
	Attachments []Attachment `json:"attachments,omitempty"`
	Resume      string       `json:"resume,omitzero"`
}

// ReplayStart selects retained history, never live input or a client workspace.
type ReplayStart struct {
	Session           string   `json:"session"`
	Speed             *float64 `json:"speed,omitzero"`
	Seed              *uint64  `json:"seed,omitzero"`
	ChildReplySummary *bool    `json:"childReplySummary,omitzero"`
}

// ValidateSelection applies the advertised catalog, including provider defaults.
func (o StartOptions) ValidateSelection(model, effort string) error {
	for _, group := range o.Models {
		for _, choice := range group.Models {
			if choice.ID != model {
				continue
			}
			if effort != "" && !slices.Contains(choice.Efforts, effort) {
				return errors.New("Choose an offered reasoning effort for this model.")
			}
			return nil
		}
	}
	return errors.New("Choose an offered model.")
}
