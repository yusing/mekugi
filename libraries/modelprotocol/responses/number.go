// Source: capturer/content_tokens.go:86:112@91f6a320.
package responses

import (
	"math/big"
	"strings"
)

// CanonicalNumber normalizes a valid JSON number without float rounding or
// exponent-sized allocation. The caller must validate JSON syntax first.
func CanonicalNumber(value string) string {
	sign := ""
	if strings.HasPrefix(value, "-") {
		sign = "-"
		value = value[1:]
	}
	mantissa, exponent, _ := strings.Cut(strings.ToLower(value), "e")
	var power big.Int
	if exponent != "" {
		power.SetString(exponent, 10)
	}
	if dot := strings.IndexByte(mantissa, '.'); dot >= 0 {
		power.Sub(&power, big.NewInt(int64(len(mantissa)-dot-1)))
		mantissa = mantissa[:dot] + mantissa[dot+1:]
	}
	mantissa = strings.TrimLeft(mantissa, "0")
	if mantissa == "" {
		return "0"
	}
	digits := strings.TrimRight(mantissa, "0")
	power.Add(&power, big.NewInt(int64(len(mantissa)-len(digits))))
	if power.Sign() == 0 {
		return sign + digits
	}
	return sign + digits + "e" + power.String()
}
