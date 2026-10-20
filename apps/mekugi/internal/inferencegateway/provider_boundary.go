package inferencegateway

// Source: apps/mekugi/internal/router/provider_boundary.go:1:169@[bd606c09] configured-provider policy.

import (
	"errors"
	"slices"
	"strings"

	"github.com/yusing/mekugi/providers/grok"

	"github.com/yusing/mekugi/providers/opencode"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

const providerNoHostedSearchInstruction = "\nOpenAI-hosted web search is unavailable on this route. Use only the provided tools; do not claim to have performed unavailable searches."

// codecDiagnostics adds the selected route identity to application diagnostics.
func codecDiagnostics(d codexresponses.Diagnostics, name, subject, prefix, contentCode string) codexresponses.Diagnostics {
	d.Name, d.Subject, d.Prefix, d.ContentErrorCode = name, subject, prefix, contentCode
	d.BufferBytes = codexresponses.MaxResponseBytes
	return d
}

func providerCodecPolicy(d codexresponses.Diagnostics) codexresponses.Policy {
	return codexresponses.Policy{Diagnostics: d,
		NoHostedSearchInstruction: providerNoHostedSearchInstruction,
	}
}

// collaborationGuidance resolves invocation-specific provider advice without
// changing native collaboration schemas or choosing tool eligibility.
func collaborationGuidance(grokEnabled, grokUnprefixed bool, openCode ...string) codexresponses.CollaborationGuidance {
	var guidance codexresponses.CollaborationGuidance
	if grokEnabled {
		modelNote := strings.ReplaceAll(grokModelInstruction, "%MODELS%", "`grok:"+strings.Join(grok.Models(), "`, `grok:")+"`")
		if grokUnprefixed {
			modelNote = strings.ReplaceAll(modelNote, "grok:", "")
		}
		guidance.SpawnArguments = map[string]string{
			"model":            modelNote,
			"fork_turns":       grokForkTurnsInstruction,
			"reasoning_effort": grokReasoningEffortInstruction,
		}
	}
	if len(openCode) > 0 {
		guidance.SpawnDescription = strings.ReplaceAll(opencodeSpawnInstruction, "%MODELS%", strings.Join(openCode, ", "))
	}
	return guidance
}

// providerRequest holds transport decisions separately from the opaque codec.
type providerRequest struct {
	body   map[string]any
	format string
	stream bool
	codec  *codexresponses.Stream
}

func grokTranslationPolicy(d codexresponses.Diagnostics) codexresponses.Policy {
	// Mixed refusal/text output keeps its legacy shared content error code.
	p := providerCodecPolicy(codecDiagnostics(d, "Grok", "grok", "grok", "opencode_stream_invalid"))
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

func openCodeTranslationPolicy(service *opencode.Service, model, format string, d codexresponses.Diagnostics) codexresponses.Policy {
	p := providerCodecPolicy(codecDiagnostics(d, "OpenCode", "OpenCode", "opencode", "opencode_stream_invalid"))
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
func translateProviderRequest(body []byte, service *opencode.Service, d codexresponses.Diagnostics) (_ *providerRequest, err error) {
	var policy codexresponses.Policy
	if service != nil {
		// OpenCode exposes request compatibility errors even for malformed JSON.
		// Existing structured diagnostics pass through without text/code rewriting.
		defer func() {
			if err != nil {
				if _, ok := errors.AsType[*CompatibilityError](err); !ok {
					err = IncompatibleRequest("opencode_unsupported_request", err.Error())
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
		policy = grokTranslationPolicy(d)
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
		policy = openCodeTranslationPolicy(service, model, format, d)
	}
	wire, codec, err := codexresponses.Translate(request.Fields, model, format, policy)
	if err != nil {
		return nil, err
	}
	return &providerRequest{body: wire, codec: codec, format: format, stream: request.Stream}, nil
}

// CollaborationGuidance resolves the configured invocation models without exposing
// credential or transport internals to request preparation.
func (c *Client) CollaborationGuidance() (codexresponses.CollaborationGuidance, bool) {
	var models []string
	for _, prefix := range []string{"opencode-go", "opencode-zen"} {
		if client := c.OpenCode[prefix]; client != nil {
			for _, model := range client.OpenCode.Models() {
				models = append(models, prefix+":"+model.ID)
			}
		}
	}
	enabled := c.Grok != nil
	return collaborationGuidance(enabled, enabled && c.Grok.Unprefixed, models...), enabled || len(models) > 0
}
