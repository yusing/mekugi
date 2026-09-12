package mekugi

import "context"

// syntaxCascadeOrigins reduces normalized diagnostic lines to original diagnostic
// indices. Parsers own payload projection and occurrence accounting; this reducer
// owns earlier-location search, one reparse per repair line, and cancellation.
func syntaxCascadeOrigins(ctx context.Context, content string, lines []int, parseLines func(string) []int) []int {
	locations := make([]int, 0, len(lines))
	origins := make([]int, 0, len(lines))
	remainingByRepairLine := make(map[int]map[int]struct{})
	for index, line := range lines {
		if ctx.Err() != nil {
			return origins
		}
		origin, mapped := index, false
		for _, location := range locations {
			repairLine := lines[location]
			// Distinct diagnostics on one line retain their own columns/payload.
			if line == repairLine {
				mapped = true
				break
			}
			if repairLine < 1 {
				continue
			}
			remaining, ok := remainingByRepairLine[repairLine]
			if !ok {
				if ctx.Err() != nil {
					return origins
				}
				candidate, validLine := blankGeneratedLine(content, repairLine)
				if validLine {
					remaining = make(map[int]struct{})
					for _, remainingLine := range parseLines(candidate) {
						remaining[remainingLine] = struct{}{}
					}
				}
				remainingByRepairLine[repairLine] = remaining
			}
			if _, remains := remaining[line]; !remains {
				origin, mapped = location, true
				break
			}
		}
		if !mapped {
			locations = append(locations, index)
		}
		origins = append(origins, origin)
	}
	return origins
}

// blankGeneratedLine blanks the content of a specific line in the generated source.
func blankGeneratedLine(content string, line int) (string, bool) {
	lines := renderedLines(content)
	if line < 1 || line > len(lines) {
		return "", false
	}
	current := lines[line-1]
	candidate := []byte(content)
	for index := current.Start; index < current.ContentEnd; index++ {
		candidate[index] = ' '
	}
	return string(candidate), true
}
