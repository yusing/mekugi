package changes

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

// Source: internal/router/mekugi_changes.go:357:387@[1a1981ba] change identities.
func ID(stream string, number int) string {
	var name strings.Builder
	for _, letter := range stream {
		name.WriteString(history.HandleWord(int(letter - 'a')))
	}
	name.WriteString(strconv.Itoa(number))
	return name.String()
}

func ParseID(id string) (stream string, number int, err error) {
	tail := id
	for {
		matched := false
		for index := range 26 {
			word := history.HandleWord(index)
			if rest, found := strings.CutPrefix(tail, word); found {
				stream += string(rune('a' + index))
				tail, matched = rest, true
				break
			}
		}
		if !matched {
			break
		}
	}
	number, err = strconv.Atoi(tail)
	if stream == "" || err != nil || number < 1 || strconv.Itoa(number) != tail {
		return "", 0, fmt.Errorf("invalid change ID %q", id)
	}
	return stream, number, nil
}
