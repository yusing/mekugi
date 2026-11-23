package binding

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
)

type ShellGuardKind string

const (
	RemoteWrite ShellGuardKind = "remoteWrite"
	Sudo        ShellGuardKind = "sudo"
)

type ShellGuardSecretKind string

const (
	SudoPassword  ShellGuardSecretKind = "sudoPassword"
	GpgPassphrase ShellGuardSecretKind = "gpgPassphrase"
)

// ShellGuardApprovalParams decodes native v2 `shellGuard/requestApproval` server requests.
type ShellGuardApprovalParams struct {
	ThreadID    string         `json:"threadId"`
	TurnID      string         `json:"turnId"`
	ItemID      string         `json:"itemId"`
	StartedAtMs int64          `json:"startedAtMs"`
	Kind        ShellGuardKind `json:"kind"`
	Cwd         string         `json:"cwd"`
	Executable  string         `json:"executable"`
	Argv        []string       `json:"argv"`
}

func DecodeShellGuardApproval(params jsontext.Value) (ShellGuardApprovalParams, error) {
	var p ShellGuardApprovalParams
	if err := json.Unmarshal(params, &p); err != nil {
		return p, err
	}
	if p.Kind != RemoteWrite && p.Kind != Sudo {
		return p, errors.New("unsupported shellGuard/requestApproval kind")
	}
	if len(p.Argv) == 0 {
		return p, errors.New("shellGuard/requestApproval missing argv")
	}
	return p, nil
}

// ShellGuardApprovalResponse encodes client responses to `shellGuard/requestApproval`.
type ShellGuardApprovalResponse struct {
	Decision string  `json:"decision"`
	Reason   *string `json:"reason"`
}

// ShellGuardSecretParams decodes native v2 `shellGuard/requestSecret` server requests.
type ShellGuardSecretParams struct {
	ThreadID    string               `json:"threadId"`
	TurnID      string               `json:"turnId"`
	ItemID      string               `json:"itemId"`
	StartedAtMs int64                `json:"startedAtMs"`
	Kind        ShellGuardSecretKind `json:"kind"`
	Cwd         string               `json:"cwd"`
	Executable  string               `json:"executable"`
	Argv        []string             `json:"argv"`
}

func DecodeShellGuardSecret(params jsontext.Value) (ShellGuardSecretParams, error) {
	var p ShellGuardSecretParams
	if err := json.Unmarshal(params, &p); err != nil {
		return p, err
	}
	if p.Kind != SudoPassword && p.Kind != GpgPassphrase {
		return p, errors.New("unsupported shellGuard/requestSecret kind")
	}
	if len(p.Argv) == 0 {
		return p, errors.New("shellGuard/requestSecret missing argv")
	}
	return p, nil
}

// ShellGuardSecretResponse encodes client responses to `shellGuard/requestSecret`.
type ShellGuardSecretResponse struct {
	Decision string  `json:"decision"`
	Secret   *string `json:"secret"`
	Reason   *string `json:"reason"`
}
