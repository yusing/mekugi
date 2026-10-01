package router

func testOpenCodeTranslation(service *openCodeService, model, format string, tools map[string]providerTool) *providerTranslation {
	var endpoint providerEndpoint = chatEndpoint{}
	switch format {
	case "anthropic":
		endpoint = messagesEndpoint{}
	case "responses":
		endpoint = responsesEndpoint{}
	}
	return &providerTranslation{policy: openCodeTranslationPolicy(service, model, format), endpoint: endpoint, body: map[string]any{"model": model}, tools: tools}
}

func isResponsesEndpoint(endpoint providerEndpoint) bool {
	_, ok := endpoint.(responsesEndpoint)
	return ok
}
