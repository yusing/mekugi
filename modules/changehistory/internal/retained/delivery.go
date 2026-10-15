package retained

// Source: internal/router/mchanges_delivery.go@[1a1981ba] retained change behavior.

import (
	"crypto/sha256"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/yusing/mekugi/contracts/command"
)

// Source: internal/router/mchanges.go:672:747@[7d2e17ba] read and mutation delivery.
type changeReadDelivery struct {
	snapshot       changeReadSnapshot
	text, selected string
}

func (*changeReadDelivery) ReferenceCount() int { return 1 }

func (p *changeReadDelivery) Prepare(references []string) (command.PreparedDelivery, error) {
	if len(references) != 1 {
		return command.PreparedDelivery{}, errors.New("change read requires one recovery reference")
	}
	snapshot := p.snapshot
	snapshot.Offset, snapshot.Digest, snapshot.Frozen = len(p.selected), fmt.Sprintf("%x", sha256.Sum256([]byte(p.text))), true
	data, err := json.Marshal(&snapshot, jsonv1.DefaultOptionsV1())
	if err != nil {
		return command.PreparedDelivery{}, err
	}
	output := command.Output{Stdout: p.selected, ExitCode: 1}
	prepared := command.PreparedDelivery{Referenced: output, Unreferenced: output, Recovery: []command.RecoveryOutput{{ChangeSelection: data}}}
	prepared.Referenced.Stderr = "read: incomplete; next_call: mread " + references[0] + "\n"
	return prepared, nil
}

type changeMutationDelivery struct {
	report, selected string
	status           int
}

func (*changeMutationDelivery) ReferenceCount() int { return 1 }

func (p *changeMutationDelivery) Prepare(references []string) (command.PreparedDelivery, error) {
	if len(references) != 1 {
		return command.PreparedDelivery{}, errors.New("change report requires one recovery reference")
	}
	output := command.Output{Stdout: p.selected, ExitCode: 1}
	prepared := command.PreparedDelivery{Referenced: output, Unreferenced: output, Recovery: []command.RecoveryOutput{{OmittedOutput: command.OmittedOutput{Stdout: p.report[len(p.selected):], StdoutKind: "rows"}, ExitCode: 1}}}
	prepared.Referenced.Stderr = "read: incomplete; next_call: mread " + references[0] + "\n"
	return prepared, nil
}

func (p *changeMutationDelivery) RecoveryFailed(err error) command.Output {
	return command.Output{Stdout: p.report, Stderr: "mchanges: report not paged: " + err.Error() + "\n", ExitCode: p.status}
}
