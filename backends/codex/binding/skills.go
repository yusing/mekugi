package binding

// SkillCatalog is the native skills/list result shared by picker presentation
// and name resolution. The host owns discovery, enablement and source identity.
type SkillCatalog struct {
	Data []struct {
		Cwd    string `json:"cwd"`
		Skills []struct {
			Name             string `json:"name"`
			Path             string `json:"path"`
			Description      string `json:"description"`
			ShortDescription string `json:"shortDescription"`
			Interface        *struct {
				ShortDescription string `json:"shortDescription"`
				DisplayName      string `json:"displayName"`
			} `json:"interface"`
			Enabled bool `json:"enabled"`
		} `json:"skills"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	} `json:"data"`
}
