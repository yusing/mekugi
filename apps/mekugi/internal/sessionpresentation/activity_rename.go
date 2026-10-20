package sessionpresentation

// Source: apps/mekugi/internal/router/app_server_thread_metadata.go:38:82@7166de95.
func (v *ActivitySource) RenameCaller(old, name string) {
	if old == name {
		return
	}
	for i := range v.Entries {
		v.Model.Mutate(i, func(entry *ActivityRecord) {
			if entry.Source.Agent == old {
				entry.Source.Agent = name
			}
			if entry.Source.Assignment != nil {
				if entry.Source.Assignment.From == old {
					entry.Source.Assignment.From = name
				}
				if entry.Source.Assignment.To == old {
					entry.Source.Assignment.To = name
				}
			}
			// Preserve merged exits, filters and answer links while renaming
			// only the display identities in already-parsed blocks.
			for j := range entry.Values {
				block := &entry.Values[j]
				if block.From == old {
					block.From = name
				}
				if block.To == old {
					block.To = name
				}
				if block.Owner == old {
					block.Owner = name
				}
			}
		})
	}
	v.RenameRetiredSkills(old, name)
}
