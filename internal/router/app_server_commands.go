package router

import "slices"

// Only commands implemented by the native shell belong in this catalog.
var nativeCommands = []composerChoice{
	{name: "/model", description: "Choose the model"},
	{name: "/reasoning", description: "Choose the reasoning effort"},
	{name: "/tier", description: "Choose the service tier"},
	{name: "/skills", description: "List or manage skills"},
	{name: "/status", description: "Show session settings and usage limits"},
	{name: "/copy", description: "Copy the last response or part of it"},
	{name: "/quit", description: "Quit the session"},
}

func (u *appServerUI) filterCommands(query string) {
	p := &u.picker
	previous := ""
	if p.selected < len(p.choices) {
		previous = p.choices[p.selected].name
	}
	p.choices, p.loading, p.problem = nil, false, ""
	for _, command := range nativeCommands {
		if _, ok := pickerMatchScore(command.name[1:], query); ok {
			p.choices = append(p.choices, command)
		}
	}
	slices.SortStableFunc(p.choices, func(a, b composerChoice) int {
		aScore, _ := pickerMatchScore(a.name[1:], query)
		bScore, _ := pickerMatchScore(b.name[1:], query)
		return aScore - bScore
	})
	p.selected = max(0, slices.IndexFunc(p.choices, func(c composerChoice) bool { return c.name == previous }))
}
