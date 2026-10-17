package router

import (
	"cmp"
	"strings"

	"github.com/yusing/mekugi/backends/codex/binding"

	"github.com/yusing/mekugi/libraries/sourceanalysis/markdownsyntax"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

func appServerToolText(item binding.ThreadItem, cwd string) string {
	if item.Type != "webSearch" {
		return appServerCommandText(item, cwd)
	}
	query := item.Query
	if action := item.Action; action != nil {
		switch action.Type {
		case "openPage":
			return "Open " + markdownsyntax.QuoteCode(action.URL)
		case "findInPage":
			return "Search " + markdownsyntax.QuoteCode(action.Pattern) + " in " + markdownsyntax.QuoteCode(action.URL)
		case "search":
			query = cmp.Or(strings.Join(action.Queries, " | "), action.Query, query)
		}
	}
	return "Search " + markdownsyntax.QuoteCode(query)
}

// Count returned records only when host evidence is complete and attributable
// to one search. Missing output, errors, context rows and truncated output are
// not zero results. No command is executed to infer a count.
func appServerSearchResults(item binding.ThreadItem) *int {
	if item.Type == "webSearch" {
		if item.Results != nil {
			return new(len(item.Results))
		}
		return nil
	}
	if item.Type != "commandExecution" || item.Status != "completed" || item.ExitCode == nil || *item.ExitCode < 0 || *item.ExitCode > 1 {
		return nil
	}
	// Codex's shell aggregate can silently cap at 1 MiB.
	// Source: codex-rs/core/src/exec.rs:865:910 (DEFAULT_OUTPUT_BYTES_CAP).
	if item.AggregatedOutput != nil && len(*item.AggregatedOutput) >= 1<<20 {
		return nil
	}
	return activitypresentation.SearchResultCount(appServerDisplayCommand(item.Command), item.AggregatedOutput, *item.ExitCode)
}
