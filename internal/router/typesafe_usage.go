package router

import (
	"fmt"
	"strings"
)

// TypeSafe consumption has its own provider bucket. It must not change an
// agent's active model, mentor schedule, cache counters, or estimated API cost.
func (u *threadUsage) addTypesafe(thread string, usage typesafeUsage) {
	if u == nil || thread == "" || len(thread) > maxCommentaryPublicationBytes {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return
	}
	if u.typesafe == nil {
		u.typesafe = make(map[string]typesafeUsage)
	}
	total := u.typesafe[thread]
	total.add(usage)
	u.typesafe[thread] = total
}

func writeTypesafeUsageReport(text *strings.Builder, report tokenUsageReport, rows []agentTokenUsage) {
	if report.typesafe.Requests == 0 && !report.typesafe.Incomplete {
		return
	}
	text.WriteString("\n\n## TypeSafe AI usage\n\nAgent-model totals above exclude this auxiliary provider. Counts below are provider-reported, not local output-reduction estimates.\n\n| Agent | Model | Requests | Input | Output | Missing usage | Cost |\n| --- | --- | ---: | ---: | ---: | ---: | --- |\n")
	row := func(agent string, usage typesafeUsage) {
		if usage.Incomplete {
			fmt.Fprintf(text, "| %s | %s | n/a | n/a | n/a | n/a | n/a |\n", tokenUsageCell(agent), typesafeModel)
			return
		}
		fmt.Fprintf(text, "| %s | %s | %d | %s | %s | %d | n/a |\n", tokenUsageCell(agent), typesafeModel,
			usage.Requests, formatUsageTokens(usage.InputTokens), formatUsageTokens(usage.OutputTokens), usage.MissingResponses)
	}
	for _, agent := range rows {
		if agent.report.typesafe.Requests != 0 || agent.report.typesafe.Incomplete {
			row(agentDisplayName(agent.agent), agent.report.typesafe)
		}
	}
	row("Total TypeSafe", report.typesafe)
	text.WriteString("\nRouter-lifetime TypeSafe HTTP attempts, including retries and judgments that keep output unchanged. Missing usage counts attempts without usable provider token counts, not zero-token calls. No TypeSafe price is configured; cost and net monetary savings are unavailable.")
	if report.typesafe.Incomplete {
		text.WriteString(" Usage totals unavailable due to accounting overflow.")
	}
}
