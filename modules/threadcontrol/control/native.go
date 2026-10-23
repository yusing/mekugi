package control

import (
	"context"
	"encoding/json/jsontext"
)

// Native is the server's semantic thread API. The execution owner chooses host
// requests and confirms their results; collaboration never dispatches wire RPCs.
type Native interface {
	Runtime(string) (Runtime, error)
	Resume(context.Context, string) (Runtime, error)
	Confirm(context.Context, NativeInput) error
	Create(context.Context, string, string, Settings) (Receipt, error)
}

// Runtime is one admitted native execution owner's typed operations.
type Runtime interface {
	Thread(context.Context, string) (ThreadInfo, error)
	Configuration(context.Context, string, string) (jsontext.Value, error)
	Input(context.Context, NativeInput) (string, error)
	Interrupt(context.Context, string, string) error
	Confirm(context.Context, NativeInput) error
}

type ThreadInfo struct {
	Thread   string  `json:"id"`
	Session  string  `json:"sessionId"`
	Path     string  `json:"path"`
	Provider string  `json:"modelProvider"`
	Parent   *string `json:"parentThreadId"`
	Fork     *string `json:"forkedFromId"`
}

// NativeInput preserves agent attribution separately from human composer input.
// Run starts or steers work; a retained-only message leaves idle work idle.
type NativeInput struct {
	Thread, ParentTurn, TargetTurn, ID, Sender, Recipient, Kind, Message string
	Run                                                                  bool
	Settings                                                             *Settings
	Wake                                                                 *WaitResult
}
