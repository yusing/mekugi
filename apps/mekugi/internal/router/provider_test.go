package router

import (
	"testing"

	"github.com/yusing/mekugi/providers/opencode"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

func testProviderRequest(t *testing.T, policy codexresponses.Policy, model, format string, tools []any) *providerRequest {
	t.Helper()
	request, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{"input": []any{}, "tools": tools}))
	if err != nil {
		t.Fatal(err)
	}
	body, codec, err := codexresponses.Translate(request.Fields, model, format, policy)
	if err != nil {
		t.Fatal(err)
	}
	return &providerRequest{body: body, format: format, codec: codec}
}

func testOpenCodeTranslation(t *testing.T, service *opencode.Service, model, format string, tools []any) *providerRequest {
	return testProviderRequest(t, openCodeTranslationPolicy(service, model, format), model, format, tools)
}

func grokTestFailureDetail(body []byte) string { return newProviderHTTPError("Grok", 0, body).Error() }
func openCodeTestFailureDetail(body []byte) string {
	return newProviderHTTPError("OpenCode", 0, body).Error()
}
