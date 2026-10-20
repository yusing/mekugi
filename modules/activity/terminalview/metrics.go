package terminalview

import (
	"fmt"
	"strings"
)

func FormatUsageTokens(count uint64) string {
	for _, unit := range []struct {
		size   uint64
		suffix string
	}{{1_000_000_000, "B"}, {1_000_000, "M"}, {1_000, "K"}} {
		if count >= unit.size {
			return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(count)/float64(unit.size)), ".0") + unit.suffix
		}
	}
	return fmt.Sprint(count)
}

func SkillLabel(names []string) string {
	if len(names) == 1 {
		return "1 loaded skill"
	}
	return fmt.Sprintf("%d loaded skills", len(names))
}
