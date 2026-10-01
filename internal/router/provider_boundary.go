package router

import (
	"errors"
	"slices"
	"strings"
)

// Preserve the existing wire instruction on both providers; diagnostic identity
// does not rewrite provider-visible prompt content.
const providerNoHostedSearchInstruction = "\nOpenAI-hosted web search is unavailable on this Grok route. Use only the provided tools; do not claim to have performed unavailable searches."

// translationPolicy is resolved once at the authenticated provider boundary.
// Protocol adapters and the Responses emitter never infer it from an endpoint.
type translationPolicy struct {
	diagnostics                 providerDiagnostics
	completionIncludesReasoning bool
	replaySummaries             bool
	closeFailedStream           bool
	noHostedSearchInstruction   string
	restoreReasoning            func(string) (map[string]any, error)
	sealReasoning               func(any) string
	resolveEffort               func(string) (string, error)
}

type providerDiagnostics struct {
	name, subject, prefix string
	contentErrorCode      string
}

func (d providerDiagnostics) streamError(code, message string) error {
	return staticCriticalDiagnostic(d.prefix+"_stream_"+code, message)
}

func (d providerDiagnostics) contentError(message string) error {
	return staticCriticalDiagnostic(d.contentErrorCode, message)
}

func grokTranslationPolicy() translationPolicy {
	return translationPolicy{
		// Mixed refusal/text output historically uses this shared content code.
		// Keep that diagnostic contract explicit rather than rebranding errors.
		diagnostics:               providerDiagnostics{name: "Grok", subject: "grok", prefix: "grok", contentErrorCode: "opencode_stream_invalid"},
		noHostedSearchInstruction: providerNoHostedSearchInstruction,
		resolveEffort: func(effort string) (string, error) {
			switch effort {
			case "", "low", "medium", "high", "xhigh":
				return effort, nil
			default:
				return "", errors.New("grok supports reasoning effort low, medium, high or xhigh")
			}
		},
	}
}

func openCodeTranslationPolicy(service *openCodeService, model, format string) translationPolicy {
	p := translationPolicy{
		diagnostics:                 providerDiagnostics{name: "OpenCode", subject: "OpenCode", prefix: "opencode", contentErrorCode: "opencode_stream_invalid"},
		completionIncludesReasoning: true,
		replaySummaries:             true,
		// Preserve the existing provider-visible prompt independently of diagnostics.
		noHostedSearchInstruction: providerNoHostedSearchInstruction,
		resolveEffort: func(effort string) (string, error) {
			// A model switch can inherit an effort unsupported by this model.
			if slices.Contains(service.efforts(model), effort) {
				return effort, nil
			}
			return "", nil
		},
	}
	if format != "chat" {
		p.closeFailedStream = true
		p.restoreReasoning = func(value string) (map[string]any, error) { return restoreOpenCodeReasoning(service, model, value) }
		p.sealReasoning = func(item any) string { return sealOpenCodeReasoning(service, model, item) }
	}
	return p
}

// translateProviderRequest selects identity and capabilities before entering the
// shared validator. The service is already pinned by the transport boundary.
func translateProviderRequest(body []byte, service *openCodeService) (_ *providerTranslation, err error) {
	policy := grokTranslationPolicy()
	if service != nil {
		// OpenCode exposes request compatibility errors even for malformed JSON.
		// Existing structured diagnostics pass through without text/code rewriting.
		defer func() {
			if err != nil {
				if _, ok := errors.AsType[*requestCompatibilityError](err); !ok {
					err = incompatibleRequest("opencode_unsupported_request", err.Error())
				}
			}
		}()
	}
	request, err := parseResponsesRequest(body)
	if err != nil {
		return nil, err
	}
	model, supported := grokProviderModel(request.model())
	var endpoint providerEndpoint = chatEndpoint{}
	if service == nil {
		if !supported {
			return nil, errors.New("unsupported Grok model")
		}
	} else {
		var ok bool
		model, ok = strings.CutPrefix(request.model(), service.prefix+":")
		_, supported = service.model(model)
		if !ok || !supported {
			return nil, errors.New("unsupported OpenCode model")
		}
		// Preserve the explicit-history rejection before catalog-format validation.
		if jsonString(request.fields, "previous_response_id") != "" {
			return nil, errors.New("OpenCode requires explicit conversation history, not previous_response_id")
		}
		format := service.format(model)
		switch format {
		case "chat":
			endpoint = chatEndpoint{}
		case "anthropic":
			endpoint = messagesEndpoint{}
		case "responses":
			endpoint = responsesEndpoint{}
		default:
			return nil, errors.New("OpenCode model API format is missing or unsupported in the online catalog")
		}
		policy = openCodeTranslationPolicy(service, model, format)
	}
	return translateResponsesForProvider(request, model, policy, endpoint)
}
