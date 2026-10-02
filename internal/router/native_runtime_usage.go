package router

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

func (u *appServerUI) showRuntimeUsage() {
	u.deleteDraftRange(0, len(u.draft))
	p := &appServerStatusReport{title: "Claude · Usage"}
	u.statusPanel, u.runtime.usagePanel = p, p
	u.renderRuntimeUsage()
}

// Reuse the structured status dialog, without router transport metrics or a
// token-count API call. Cost is a native API-equivalent estimate, not billing.
func (u *appServerUI) renderRuntimeUsage() {
	r := u.runtime
	p := r.usagePanel
	if p == nil || p != u.statusPanel {
		return
	}
	p.fields, p.groups = nil, nil
	add := func(group, label, value string) {
		if !slices.Contains(p.groups, group) {
			p.groups = append(p.groups, group)
		}
		p.fields = append(p.fields, statusField{group: group, label: label, value: value})
	}
	if r.usage == nil {
		add("Usage", "", "No native usage report received")
	} else {
		add("Usage", "Scope", "Latest cumulative native query totals, including subagents and retained turns")
		if r.usage.CostUSD != nil {
			add("Usage", "API-equivalent estimate", fmt.Sprintf("$%.4f · not subscription charges", *r.usage.CostUSD))
		}
		for _, name := range slices.Sorted(maps.Keys(r.usage.Models)) {
			m := r.usage.Models[name]
			group := "Model · " + name
			for _, row := range []struct {
				label string
				value *uint64
			}{
				{"Input tokens", m.Input}, {"Output tokens", m.Output}, {"Thinking (included in output)", m.Thinking},
				{"Cache read tokens", m.CacheRead}, {"Cache creation tokens", m.CacheWrite}, {"Context capacity", m.ContextWindow},
			} {
				if row.value != nil {
					add(group, row.label, fmt.Sprint(*row.value))
				}
			}
		}
	}
	for _, key := range slices.Sorted(maps.Keys(r.limits)) {
		limit := r.limits[key]
		group := "Limit"
		if limit.Window != "" {
			group += " · " + strings.ReplaceAll(limit.Window, "_", " ")
		}
		if limit.Scope != "" {
			group += " · " + limit.Scope
		}
		if limit.Status != "" {
			add(group, "Status", limit.Status)
		}
		if limit.Utilization != nil {
			add(group, "Used", fmt.Sprintf("%.1f%%", *limit.Utilization*100))
		}
		if limit.ResetsAt != nil {
			add(group, "Resets", time.Unix(int64(*limit.ResetsAt), 0).Local().Format(time.RFC3339))
		}
	}
}
