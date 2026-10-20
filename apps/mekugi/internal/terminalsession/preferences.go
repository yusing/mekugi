package terminalsession

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"

	"github.com/yusing/mekugi/libraries/terminalui"
)

// Preferences is the terminal's renderer-keyed schema. Session preferences owns
// storage and applied host settings; neither depends on these terminal fields.
// Source: apps/mekugi/internal/router/app_server_panes.go:12:71@70e7ce50 nativePaneState
type Preferences struct {
	Focus               int  `json:"focus"`
	Split               int  `json:"split"`
	DiffOpen            bool `json:"diffOpen"`
	InterruptLocked     bool `json:"interruptLocked,omitzero"`
	PredictionsDisabled bool `json:"predictionsDisabled,omitzero"`
	NavigatorColumns    int  `json:"navigatorColumns"`
}

func ReadPreferences(data jsontext.Value) (Preferences, error) {
	var state Preferences
	if data == nil {
		return state, nil
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return Preferences{}, err
	}
	if state.Focus < 0 || state.Focus > 3 || state.Split < 0 || state.Split > 10000 || state.NavigatorColumns < 0 || state.NavigatorColumns > 10000 || state.Focus == 1 && !state.DiffOpen || state.Focus == 2 && state.DiffOpen {
		return Preferences{}, errors.New("invalid or unsupported pane state")
	}
	return state, nil
}

// Preferences captures only terminal state, independently of its application binding.
// Source: apps/mekugi/internal/router/app_server_panes.go:13:24@b74f44de.
func (s *Shell) Preferences(main *Main) Preferences {
	s.Clamp()
	state := Preferences{Focus: s.Focus, Split: s.Split, DiffOpen: s.DiffOpen}
	if navigator, ok := s.Auxiliary.(terminalui.PaneNavigator); ok {
		state.NavigatorColumns = navigator.NavigatorColumns()
	}
	if main != nil {
		state.InterruptLocked = main.InterruptLocked
		state.PredictionsDisabled = main.PredictionsDisabled
	}
	return state
}

// Source: apps/mekugi/internal/router/app_server_panes.go:66:72@b74f44de.
func (s *Shell) RestorePreferences(state Preferences, main *Main) {
	s.Focus, s.Split, s.DiffOpen = state.Focus, state.Split, state.DiffOpen
	if s.Focus == 2 || s.Focus == 1 && !s.DiffOpen {
		s.Focus = 0
	}
	if main != nil {
		main.InterruptLocked = state.InterruptLocked
		main.PredictionsDisabled = state.PredictionsDisabled
	}
	s.Clamp()
	if navigator, ok := s.Auxiliary.(terminalui.PaneNavigator); ok {
		navigator.SetNavigatorColumns(state.NavigatorColumns)
	}
}

// PreferenceEncoder caches only terminal encoding, avoiding work on unchanged frames.
type PreferenceEncoder struct {
	state   Preferences
	encoded jsontext.Value
}

func (e *PreferenceEncoder) Encode(state Preferences) (jsontext.Value, error) {
	if e.encoded == nil || state != e.state {
		encoded, err := json.Marshal(&state)
		if err != nil {
			return nil, err
		}
		e.state, e.encoded = state, encoded
	}
	return e.encoded, nil
}
