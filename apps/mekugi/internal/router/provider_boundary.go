package router

import (
	"errors"
	"slices"
	"strings"

	"github.com/yusing/mekugi/providers/grok"

	"github.com/yusing/mekugi/providers/opencode"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

// Preserve the existing wire instruction on both providers; diagnostic identity
// does not rewrite provider-visible prompt content.
const providerNoHostedSearchInstruction = "\nOpenAI-hosted web search is unavailable on this Grok route. Use only the provided tools; do not claim to have performed unavailable searches."

// providerCodecDiagnostics preserves router error identity at the codec boundary.
func providerCodecDiagnostics(name, subject, prefix, contentCode string) codexresponses.Diagnostics {
	return codexresponses.Diagnostics{
		Name: name, Subject: subject, Prefix: prefix, ContentErrorCode: contentCode,
		BufferBytes: codexresponses.MaxResponseBytes,
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

func providerCodecPolicy(d codexresponses.Diagnostics) codexresponses.Policy {
	return codexresponses.Policy{Diagnostics: d,
		NoHostedSearchInstruction: providerNoHostedSearchInstruction,
		CustomInputInstruction:    grokInputInstruction,
		ExecInstruction:           grokExecInstruction,
		FormatInstructionPrefix:   grokFormatPrefixInstruction,
	}
}

// providerRequest holds transport decisions separately from the opaque codec.
type providerRequest struct {
	body   map[string]any
	format string
	stream bool
	codec  *codexresponses.Stream
}

func grokTranslationPolicy() codexresponses.Policy {
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

func openCodeTranslationPolicy(service *opencode.Service, model, format string) codexresponses.Policy {
	p := providerCodecPolicy(providerCodecDiagnostics("OpenCode", "OpenCode", "opencode", "opencode_stream_invalid"))
	p.CompletionIncludesReasoning = true
	p.ReplaySummaries = true
	p.ResolveEffort = func(effort string) (string, error) {
		// A model switch can inherit an effort unsupported by this model.
		metadata, _ := service.Model(model)
		if slices.Contains(metadata.Efforts, effort) {
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
func translateProviderRequest(body []byte, service *opencode.Service) (_ *providerRequest, err error) {
	var policy codexresponses.Policy
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
	request, err := codexresponses.ParseRequest(body)
	if err != nil {
		return nil, err
	}
	model := strings.TrimPrefix(request.Model(), "grok:")
	format := "chat"
	if service == nil {
		if !slices.Contains(grok.Models(), model) {
			return nil, errors.New("unsupported Grok model")
		}
		policy = grokTranslationPolicy()
	} else {
		var ok bool
		model, ok = strings.CutPrefix(request.Model(), service.Prefix+":")
		_, supported := service.Model(model)
		if !ok || !supported {
			return nil, errors.New("unsupported OpenCode model")
		}
		// Preserve the explicit-history rejection before catalog-format validation.
		if responses.StringField(request.Fields, "previous_response_id") != "" {
			return nil, errors.New("OpenCode requires explicit conversation history, not previous_response_id")
		}
		format = service.Format(model)
		switch format {
		case "chat", "anthropic", "responses":
		default:
			return nil, errors.New("OpenCode model API format is missing or unsupported in the online catalog")
		}
		policy = openCodeTranslationPolicy(service, model, format)
	}
	wire, codec, err := codexresponses.Translate(request.Fields, model, format, policy)
	if err != nil {
		return nil, err
	}
	return &providerRequest{body: wire, codec: codec, format: format, stream: request.Stream}, nil
}
