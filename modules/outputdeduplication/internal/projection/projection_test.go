package projection

import (
	"fmt"
	"strings"
	"testing"

	expect "github.com/yusing/goutils/testing"
	"github.com/yusing/mekugi/libraries/outputbudget/tokenizer"
)

func TestTokenGateBoundary(t *testing.T) {
	codec, err := tokenizer.New()
	expect.NoError(t, err)
	count := func(text string) int {
		n, err := codec.Count(text)
		expect.NoError(t, err)
		return n
	}
	for _, ending := range []string{"\n", "\r\n", ""} {
		for _, ranged := range []bool{false, true} {
			marker, prefix := "[same as O1]", ""
			if ranged {
				marker, prefix = "[same as O10000 L1000]", strings.Repeat("source heading\n", 999)
			}
			if ending != "" {
				marker += "\n"
			}
			for _, saved := range []int{minimumTokenSavings - 1, minimumTokenSavings} {
				t.Run(fmt.Sprintf("%q/range=%v/savings=%d", ending, ranged, saved), func(t *testing.T) {
					body := strings.Repeat("x", 192)
					body += strings.Repeat(" token", saved+count(marker)-count(body+ending)) + ending
					expect.Equal(t, count(body)-count(marker), saved)
					var savings Savings
					pass := (Service{}).NewPass(&savings)
					defer pass.Close()
					if ranged {
						pass.sources = 9999
					}
					pass.Text("", prefix+body)
					expect.Nil(t, savings.codec)
					got := pass.Text("execution header\n", body)
					if saved == minimumTokenSavings {
						expect.Equal(t, got.Text, "execution header\n"+marker)
						expect.Equal(t, got.RepeatedBytes, len(body))
						expect.Equal(t, got.RepeatedLines, 1)
						expect.Equal(t, got.Occurrences, uint64(1))
					} else {
						expect.Equal(t, got.Text, fmt.Sprintf("execution header\n[O%d]\n%s", pass.sources, body))
						expect.Equal(t, got.RepeatedBytes, 0)
						expect.Equal(t, got.Occurrences, uint64(0))
					}
				})
			}
		}
	}
}

func TestTokenGateKeepsUnmeasuredAndExpandingMatches(t *testing.T) {
	for _, failed := range []bool{false, true} {
		savings := Savings{failed: failed}
		pass := (Service{}).NewPass(&savings)
		defer pass.Close()
		body := "seedword" + strings.Repeat(" ", 256) + "\n"
		if failed {
			body = strings.Repeat("valuable repeated source text ", 20) + "\n"
		}
		pass.Text("", body)
		got := pass.Text("", body)
		expect.Equal(t, got.Text, "[O2]\n"+body)
		expect.Equal(t, got.Occurrences, uint64(0))
		_, measured := savings.Result()
		expect.Equal(t, measured, !failed)
		if !failed {
			expect.True(t, savings.count(body) < savings.count("[same as O1]\n"))
		}
	}
}

func TestTokenGateDoesNotTokenizeUnmatchedText(t *testing.T) {
	body := strings.Repeat("valuable repeated source text ", 20) + "\n"
	allocations := func(tail string) float64 {
		return testing.AllocsPerRun(10, func() {
			var savings Savings
			pass := (Service{}).NewPass(&savings)
			defer pass.Close()
			pass.Text("", body)
			expect.Nil(t, savings.codec)
			got := pass.Text("", body+tail)
			expect.Equal(t, got.Text, "[O2]\n[same as O1]\n"+tail)
		})
	}
	small, large := allocations(strings.Repeat(" ", 1<<10)), allocations(strings.Repeat(" ", 128<<10))
	if large > small*1.5 {
		t.Fatalf("unmatched text grew tokenization allocations: small=%g large=%g", small, large)
	}
}

func BenchmarkTokenGate(b *testing.B) {
	for _, units := range []int{32, 256} {
		for _, low := range []bool{false, true} {
			b.Run(fmt.Sprintf("units=%d/low=%v", units, low), func(b *testing.B) {
				body := strings.Repeat("valuable repeated source text ", 20) + "\n"
				if low {
					body = strings.Repeat("x", 192) + "\n"
				}
				_, err := tokenizer.New()
				expect.NoError(b, err)
				b.ReportAllocs()
				b.SetBytes(int64(units * len(body)))
				for b.Loop() {
					var savings Savings
					pass := (Service{}).NewPass(&savings)
					for range units {
						pass.Text("", body)
					}
					pass.Close()
				}
			})
		}
	}
}
