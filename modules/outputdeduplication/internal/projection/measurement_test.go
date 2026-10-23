package projection

import (
	"testing"

	expect "github.com/yusing/goutils/testing"
)

func TestUnchangedSavingsDoesNotInitializeTokenizer(t *testing.T) {
	var savings Savings
	savings.Add("unchanged", "unchanged")
	saved, measured := savings.Result()
	expect.Equal(t, saved, int64(0))
	expect.True(t, measured)
	expect.Nil(t, savings.codec)
}
