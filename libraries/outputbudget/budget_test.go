package outputbudget

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBudgetHeadTailUnicodeAndCounting(t *testing.T) {
	var b Budget
	for _, tail := range []bool{false, true} {
		for _, limit := range []int{1, 2, 7, 42, 100} {
			for _, source := range []string{"", "short\n", strings.Repeat("first 🙂 café 中文 👨‍👩‍👧‍👦\n", 30), strings.Repeat(" ", 5000), "<|endoftext|>\r\n", "\ufeffhello world more words", "before\ufeffhello world", "\ufeff\ufeff"} {
				selected, err := b.SelectText(source, limit, tail)
				if err != nil {
					t.Fatal(err)
				}
				count, err := b.Count(selected.Text)
				if err != nil || !utf8.ValidString(selected.Text) || count > limit || selected.Tokens > limit {
					t.Fatalf("invalid token cut: %+v count=%d err=%v", selected, count, err)
				}
				if !tail && !strings.HasPrefix(source, selected.Text) || tail && !strings.HasSuffix(source, selected.Text) {
					t.Fatalf("selection tail=%t mismatch: %q", tail, selected.Text)
				}
			}
		}
	}
	for _, source := range []string{strings.Repeat(" ", 1000), strings.Repeat("representation ", 100), strings.Repeat("🙂中文\n", 100)} {
		for _, tail := range []bool{false, true} {
			selected, err := b.SelectText(source, 42, tail)
			if err != nil || len(selected.Text) > 42*4 || selected.Tokens > 42 || !utf8.ValidString(selected.Text) {
				t.Fatalf("text selection: %+v, %v", selected, err)
			}
		}
	}
	count, err := b.Count(strings.Repeat(" ", 1000))
	if err != nil || count != 250 {
		t.Fatalf("delivery count=%d, %v", count, err)
	}
	if fits, err := b.Fits("hello", 10); err != nil || !fits {
		t.Fatalf("short string should fit: fits=%t err=%v", fits, err)
	}
	if fits, err := b.Fits(strings.Repeat("a", 50), 10); err != nil || fits {
		t.Fatalf("oversized byte string should not fit: fits=%t err=%v", fits, err)
	}
}
