package router

import (
	"errors"
	"slices"
	"strings"

	"github.com/yusing/mekugi/internal/providercodec"
	"github.com/yusing/mekugi/internal/responses"
)

// Preserve the existing wire instruction on both providers; diagnostic identity
// does not rewrite provider-visible prompt content.
const providerNoHostedSearchInstruction = "\nOpenAI-hosted web search is unavailable on this Grok route. Use only the provided tools; do not claim to have performed unavailable searches."

// providerCodecDiagnostics preserves router error identity at the codec boundary.
func providerCodecDiagnostics(name, subject, prefix, contentCode string) providercodec.Diagnostics {
	return providercodec.Diagnostics{
		Name: name, Subject: subject, Prefix: prefix, ContentErrorCode: contentCode,
		BufferBytes: upstreamJSONBufferBytes,
		Error: func(code, summary, detail string) error {
			if detail == "" {
				return staticCriticalDiagnostic(code, summary)
			}
			return &criticalDiagnosticError{err: errors.New("provider reported a streaming error"), code: code, summary: summary, callerDetail: detail, distinct: true}
		},
		DescribeError: func(err error) (string, string, bool) {
			d, ok := errors.AsType[*criticalDiagnosticError](err)
			if !ok {
				return "", "", false
			}
			message := d.summary
			if d.callerDetail != "" {
				message = d.callerDetail
			}
			return d.code, message, true
		},
		Incompatible: incompatibleRequest,
		ForwardError: forwardCriticalDiagnostic,
	}
}

func providerCodecPolicy(d providercodec.Diagnostics) providercodec.Policy {
	return providercodec.Policy{Diagnostics: d,
		NoHostedSearchInstruction: providerNoHostedSearchInstruction,
		CustomInputInstruction:    embeddedInstruction("grok_input"),
		ExecInstruction:           embeddedInstruction("grok_exec"),
		FormatInstructionPrefix:   embeddedInstruction("grok_format_prefix"),
	}
}

// providerRequest holds transport decisions separately from the opaque codec.
type providerRequest struct {
	body   map[string]any
	format string
	stream bool
	codec  *providercodec.Stream
}

func grokTranslationPolicy() providercodec.Policy {
	// Mixed refusal/text output keeps its legacy shared content error code.
	p := providerCodecPolicy(providerCodecDiagnostics("Grok", "grok", "grok", "opencode_stream_invalid"))
	p.ResolveEffort = func(effort string) (string, error) {
		switch effort {
		case "", "low", "medium", "high", "xhigh":
			return effort, nil
		default:
			return "", errors.New("grok supports reasoning effort low, medium, high or xhigh")
		}
	}
	return p
}

func openCodeTranslationPolicy(service *openCodeService, model, format string) providercodec.Policy {
	p := providerCodecPolicy(providerCodecDiagnostics("OpenCode", "OpenCode", "opencode", "opencode_stream_invalid"))
	p.CompletionIncludesReasoning = true
	p.ReplaySummaries = true
	p.ResolveEffort = func(effort string) (string, error) {
		// A model switch can inherit an effort unsupported by this model.
		if slices.Contains(service.efforts(model), effort) {
			return effort, nil
		}
		return "", nil
	}
	if format != "chat" {
		p.CloseFailedStream = true
		p.RestoreReasoning = func(value string) (map[string]any, error) { return restoreOpenCodeReasoning(service, model, value) }
		p.SealReasoning = func(item any) string { return sealOpenCodeReasoning(service, model, item) }
	}
	return p
}

// translateProviderRequest selects identity and capabilities before entering the
// shared validator. The service is already pinned by the transport boundary.
func translateProviderRequest(body []byte, service *openCodeService) (_ *providerRequest, err error) {
	var policy providercodec.Policy
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
	format := "chat"
	if service == nil {
		if !supported {
			return nil, errors.New("unsupported Grok model")
		}
		policy = grokTranslationPolicy()
	} else {
		var ok bool
		model, ok = strings.CutPrefix(request.model(), service.prefix+":")
		_, supported = service.model(model)
		if !ok || !supported {
			return nil, errors.New("unsupported OpenCode model")
		}
		// Preserve the explicit-history rejection before catalog-format validation.
		if responses.StringField(request.fields, "previous_response_id") != "" {
			return nil, errors.New("OpenCode requires explicit conversation history, not previous_response_id")
		}
		format = service.format(model)
		switch format {
		case "chat", "anthropic", "responses":
		default:
			return nil, errors.New("OpenCode model API format is missing or unsupported in the online catalog")
		}
		policy = openCodeTranslationPolicy(service, model, format)
	}
	wire, codec, err := providercodec.Translate(request.fields, model, format, policy)
	if err != nil {
		return nil, err
	}
	return &providerRequest{body: wire, codec: codec, format: format, stream: request.streamResponse}, nil
}
