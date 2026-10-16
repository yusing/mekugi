package terminalsession

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
)

// Preferences is the terminal's renderer-keyed schema. Session preferences owns
// storage and applied host settings; neither depends on these terminal fields.
// Source: apps/mekugi/internal/router/app_server_panes.go:12:71@70e7ce50 nativePaneState
type Preferences struct {
	Focus            int  `json:"focus"`
	Split            int  `json:"split"`
	DiffOpen         bool `json:"diffOpen"`
	InterruptLocked  bool `json:"interruptLocked,omitzero"`
	NavigatorColumns int  `json:"navigatorColumns"`
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

func (p *Panes) Restore(state Preferences) {
	p.Focus, p.Split, p.DiffOpen = state.Focus, state.Split, state.DiffOpen
	if p.Focus == 2 || p.Focus == 1 && !p.DiffOpen {
		p.Focus = 0
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
