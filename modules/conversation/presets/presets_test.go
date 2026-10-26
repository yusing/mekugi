package presets

import (
	"strings"
	"testing"
	"testing/synctest"
)

type memoryStore [10]Slot

func (s *memoryStore) Load() ([10]Slot, error) { return *s, nil }
func (s *memoryStore) Save(slot int, value Slot) (Slot, error) {
	s[slot] = value
	return value, nil
}

func TestPresetValidation(t *testing.T) {
	for _, text := range []string{"", " ", " !rm file", strings.Repeat("é", 4001), "\xff"} {
		if err := Validate(text); err == nil {
			t.Fatalf("accepted %q", text)
		}
	}
	for _, text := range []string{" keep\n你好 ", "/model example --reasoning=high", strings.Repeat("é", 4000)} {
		if err := Validate(text); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPresetLabelValidation(t *testing.T) {
	for _, label := range []string{"two\nlines", "carriage\rreturn", "\xff"} {
		if err := ValidateLabel(label); err == nil {
			t.Fatalf("accepted %q", label)
		}
	}
	for _, label := range []string{"", "Review 你好"} {
		if err := ValidateLabel(label); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPresetFlowSaveEditRenameDeleteAndCancel(t *testing.T) {
	store := memoryStore{1: {Input: "native prompt", Label: "Native label"}}
	synctest.Test(t, func(t *testing.T) {
		var f Flow
		f.Open(&store, "draft", "/model example")
		synctest.Wait()
		if !f.Poll() || f.Problem != "" {
			t.Fatalf("open: %+v", f)
		}
		pick := func(name, input string) string {
			t.Helper()
			for _, choice := range f.Choices() {
				if choice.Name == name {
					return f.Choose(choice, input)
				}
			}
			t.Fatalf("missing %s at %s", name, f.Stage)
			return ""
		}
		if f.Choices()[0].Label() != "1  Native label" || pick("1", "") != "native prompt" {
			t.Fatal("label replaced native input")
		}
		pick("n", "")
		if f.Input != "draft" || f.Stage != Edit {
			t.Fatal("lost draft seed")
		}
		pick("continue", " ")
		if f.Input != " " || f.Problem == "" || f.Stage != Edit {
			t.Fatal("invalid input was discarded")
		}
		pick("continue", "saved\ninput")
		if f.Stage != Label || f.Query() != "" {
			t.Fatalf("new preset skipped its empty label: %+v", f)
		}
		pick("continue", "two\nlines")
		if f.Stage != Label || f.Problem == "" || f.Query() != "two\nlines" {
			t.Fatal("multi-line label was accepted or discarded")
		}
		pick("continue", "  ")
		pick("0", "")
		synctest.Wait()
		f.Poll()
		if store[0] != (Slot{Input: "saved\ninput"}) || f.Choices()[9].Label() != "0  saved" {
			t.Fatalf("empty label did not show the first line: %+v", store[0])
		}
		if got := pick("0", ""); got != "saved\ninput" {
			t.Fatalf("recall %q", got)
		}
		pick("e", "")
		pick("1", "")
		pick("continue", "changed prompt")
		if f.Stage != Label || f.Query() != "Native label" {
			t.Fatal("edit did not offer the current label")
		}
		pick("continue", "Native label")
		pick("confirm", "")
		synctest.Wait()
		f.Poll()
		if store[1] != (Slot{Input: "changed prompt", Label: "Native label"}) {
			t.Fatalf("input edit lost its label: %+v", store[1])
		}
		pick("r", "")
		pick("1", "")
		if f.Stage != Label || f.Query() != "Native label" {
			t.Fatal("rename did not offer the current label")
		}
		pick("continue", " Renamed ")
		synctest.Wait()
		f.Poll()
		if store[1] != (Slot{Input: "changed prompt", Label: "Renamed"}) || f.Stage != Quick || f.Choices()[0].Label() != "1  Renamed" {
			t.Fatalf("rename changed input or skipped saving: %+v", store[1])
		}
		pick("e", "")
		pick("0", "")
		pick("continue", "replacement")
		pick("continue", "")
		if f.Stage != Confirm {
			t.Fatal("overwrite lacked confirmation")
		}
		pick("cancel", "")
		if f.Slots[0].Input != "saved\ninput" {
			t.Fatal("cancel overwrote slot")
		}
		pick("d", "")
		pick("0", "")
		if f.Stage != Confirm {
			t.Fatal("delete lacked confirmation")
		}
		pick("confirm", "")
		synctest.Wait()
		f.Poll()
		if f.Slots[0].Input != "" {
			t.Fatal("delete failed")
		}
		pick("s", "")
		if f.Input != "/model example" {
			t.Fatal("settings seed missing")
		}
	})
}
