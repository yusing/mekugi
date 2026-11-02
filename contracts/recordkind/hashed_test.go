package recordkind

import (
	"strings"
	"testing"
)

func TestHashedName(t *testing.T) {
	for _, suffix := range []string{".json", ".json.gz", ""} {
		t.Run(suffix, func(t *testing.T) {
			match := HashedName("record-", suffix)
			hash := strings.Repeat("0123456789abcdef", 4)
			for _, tc := range []struct {
				name string
				want bool
			}{
				{"record-" + hash + suffix, true},
				{"record-" + strings.Repeat("0", 64) + suffix, true},
				{"record-" + strings.ToUpper(hash) + suffix, false},
				{"record-" + hash[:63] + suffix, false},
				{"record-" + hash + "a" + suffix, false},
				{"record-" + strings.Repeat("g", 64) + suffix, false},
				{"record-" + hash[:63] + "/" + suffix, false},
				{"record-" + hash[:63] + "\\" + suffix, false},
				{hash + suffix, false},
				{"record-" + hash + suffix + ".tmp", false},
				{"../record-" + hash + suffix, false},
			} {
				if got := match(tc.name); got != tc.want {
					t.Errorf("Match(%q) = %v, want %v", tc.name, got, tc.want)
				}
			}
		})
	}
}
