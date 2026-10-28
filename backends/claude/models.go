package claude

import (
	json "encoding/json/v2"
	"errors"
	"fmt"
)

// Model is the advertised native choice from the SDK initialization catalog.
// Availability does not establish provider authentication or entitlement.
type Model struct {
	Value         string `json:"value"`
	DisplayName   string `json:"displayName"`
	Description   string `json:"description"`
	ResolvedModel string `json:"resolvedModel,omitempty"`
}

// Models decodes only model metadata, excluding account and other initialization
// details. It sends no input or additional SDK request.
func (c *Client) Models() ([]Model, error) {
	var initialization struct {
		Models []Model `json:"models"`
	}
	if err := json.Unmarshal(c.initialization, &initialization); err != nil {
		return nil, fmt.Errorf("decode Claude model catalog: %w", err)
	}
	if initialization.Models == nil {
		return nil, errors.New("Claude SDK initialization omitted its model catalog")
	}
	for _, model := range initialization.Models {
		if model.Value == "" {
			return nil, errors.New("Claude SDK model catalog contains an empty model identifier")
		}
	}
	return initialization.Models, nil
}
