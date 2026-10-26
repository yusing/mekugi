package projection

import (
	"fmt"
	"strings"
	"testing"

	expect "github.com/yusing/goutils/testing"
	"github.com/yusing/mekugi/libraries/outputbudget/tokenizer"
	"github.com/yusing/mekugi/modules/outputdeduplication/internal/matcher"
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
			marker, prefix := "See O1:41", strings.Repeat("source heading\n", 40)
			if ranged {
				marker, prefix = "See O10000:1000", strings.Repeat("source heading\n", 999)
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
					got := pass.Text("execution header\n", body)
					if saved == minimumTokenSavings {
						expect.Equal(t, got.Text, "execution header\n"+marker)
						expect.Equal(t, got.RepeatedBytes, len(body))
						expect.Equal(t, got.RepeatedLines, 1)
						expect.Equal(t, got.Occurrences, uint64(1))
					} else {
						expect.Equal(t, got.Text, "execution header\n"+body)
						expect.Equal(t, got.RepeatedBytes, 0)
						expect.Equal(t, got.Occurrences, uint64(0))
					}
				})
			}
		}
	}
}

func TestSourceGateKeepsUnmeasuredAndLowValueText(t *testing.T) {
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
		expect.Equal(t, got.Text, body)
		expect.Equal(t, got.Occurrences, uint64(0))
		_, measured := savings.Result()
		expect.Equal(t, measured, !failed)
		if !failed {
			expect.True(t, savings.count(body)-savings.count("See O1\n") < minimumTokenSavings)
		}
	}
}

func TestSourceGateIncludesAnchorAndReference(t *testing.T) {
	for _, source := range []int{1, 10000} {
		for _, ending := range []string{"\n", "\r\n", ""} {
			for _, saved := range []int{minimumTokenSavings - 1, minimumTokenSavings} {
				t.Run(fmt.Sprintf("source=%d/ending=%q/saved=%d", source, ending, saved), func(t *testing.T) {
					var savings Savings
					anchor, reference := fmt.Sprintf("O%d:\n", source), fmt.Sprintf("See O%d", source)
					if ending != "" {
						reference += "\n"
					}
					body := strings.Repeat("x", 192)
					body += strings.Repeat(" token", saved+int(savings.count(anchor)+savings.count(reference)-savings.count(body+ending))) + ending
					pass := (Service{}).NewPass(&savings)
					defer pass.Close()
					pass.sources = source - 1
					first, repeat := pass.Text("", body), pass.Text("", body)
					if saved == minimumTokenSavings {
						expect.Equal(t, first.Text, anchor+body)
						expect.Equal(t, repeat.Text, reference)
					} else {
						expect.Equal(t, first.Text, body)
						expect.Equal(t, repeat.Text, body)
						expect.Equal(t, pass.sources, source-1)
					}
				})
			}
		}
	}
}

func TestSourceGateDoesNotTokenizeSeedlessTails(t *testing.T) {
	body := strings.Repeat("valuable repeated source text ", 20) + "\n"
	allocations := func(tail string) float64 {
		return testing.AllocsPerRun(10, func() {
			var savings Savings
			pass := (Service{}).NewPass(&savings)
			defer pass.Close()
			pass.Text("", body)
			got := pass.Text("", body+tail)
			expect.Equal(t, got.Text, "See O1\n"+tail)
		})
	}
	small, large := allocations(strings.Repeat(" ", 1<<10)), allocations(strings.Repeat(" ", 128<<10))
	if large > small*1.5 {
		t.Fatalf("unmatched text grew tokenization allocations: small=%g large=%g", small, large)
	}
}

func TestSourceGateUsesFinalRetainedWindow(t *testing.T) {
	for _, retained := range []string{"retained\n", strings.Repeat("x", 192) + "\n", strings.Repeat("valuable retained text ", 50) + "\n"} {
		var savings Savings
		pass := (Service{}).NewPass(&savings)
		defer pass.Close()
		previous := strings.Repeat("earlier reference body ", 50) + "\n"
		pass.Text("", previous)
		head := strings.Repeat("admission token ", 60) + "\n"
		tail := strings.Repeat("\n", matcher.WindowBytes-len(retained))
		got := pass.Text("", head+retained+previous+tail)
		want := head + retained + "See O1\n" + tail
		if strings.HasPrefix(retained, "valuable") {
			want = "O2:\n" + want
			expect.Equal(t, pass.Text("", retained).Text, "See O2:2\n")
		} else {
			expect.Equal(t, pass.sources, 1)
		}
		expect.Equal(t, got.Text, want)
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
