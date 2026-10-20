package executionobservation

import (
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"

	"strings"
)

func routerFaultSSE(events ...any) string {
	var stream strings.Builder
	for _, event := range events {
		stream.WriteString("data: ")
		stream.Write(responses.MustMarshalJSON(event))
		stream.WriteString("\n\n")
	}
	return stream.String()
}
