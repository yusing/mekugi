package executionobservation

import (
	"github.com/yusing/mekugi/modules/agentmetrics/evidence"

	"github.com/yusing/mekugi/modules/sessioninspection/query"
)

type sessionInspection struct {
	query.Result
	AX *evidence.InspectionReport `json:"ax,omitempty"`
}

type sessionCorpus = query.Corpus
