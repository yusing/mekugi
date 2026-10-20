package router

import "sync"

type serviceTierSettings struct {
	configured map[string]string
	choices    sync.Map // [thread, routed model] -> confirmed tier
}

func serviceTierModel(model string) string {
	if model == "gpt-5.6-terra" {
		return "gpt-6-sol"
	}
	return model
}

func (s *serviceTierSettings) choice(model, thread string) (string, bool) {
	if s != nil {
		if tier, ok := s.choices.Load([2]string{thread, serviceTierModel(model)}); ok {
			return tier.(string), true
		}
	}
	return "", false
}

// effectiveServiceTier follows request model selection and leaves host settings
// untouched. The legacy Terra selection routes to Sol before tier lookup.
func effectiveServiceTier(model, requested string, settings *serviceTierSettings, thread string) string {
	model = serviceTierModel(model)
	if settings != nil {
		if tier, ok := settings.choice(model, thread); ok {
			requested = tier
		} else if tier := settings.configured[model]; tier != "" {
			requested = tier
		}
	}
	if requested == "fast" {
		return "priority"
	}
	return requested
}
