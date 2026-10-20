package executionobservation

import (
	"net/http"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
)

func testGatewayResponse(response *http.Response, err error) (*inferencegateway.Response, error) {
	if response == nil {
		return nil, err
	}
	return &inferencegateway.Response{Response: response}, err
}

func testRequestExecutor(provider inferencegateway.Forwarder, issues *requestexecution.Failures, proxy *Preparer) requestexecution.Executor {
	executor := requestexecution.Executor{Provider: provider, Issues: issues}
	if proxy != nil {
		executor.Observations, executor.Guidance = proxy, proxy.registry.Guidance
		executor.Control, executor.Usage = proxy.control, proxy.usage
	}
	executor.Client, _ = provider.(*inferencegateway.Client)
	executor.Exchange, _ = provider.(*inferencegateway.Exchange)
	return executor
}
