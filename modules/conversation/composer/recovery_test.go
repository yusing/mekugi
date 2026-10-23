package composer

import (
	"reflect"
	"testing"
)

func TestRetainedTextPartsPreserveBindingsAndSnapshots(t *testing.T) {
	original := Draft{Text: "before @file after [Image 1] end", Files: []Token{{Start: 7, End: 12, Path: "private-file"}}, Images: []Token{{Start: 19, End: 28, Path: "private-image"}}, Attachments: []string{"frozen contents"}, AnswerImages: []string{"frozen-image"}}
	saved := original.Clone()
	if got := original.TextParts(); !reflect.DeepEqual(got, []string{"before ", "@file", " after ", "[Image 1]", " end"}) {
		t.Fatal(got)
	}
	edited, err := original.WithTextParts([]string{"改 ", " between ", "!"})
	if err != nil {
		t.Fatal(err)
	}
	if edited.Text != "改 @file between [Image 1]!" || edited.Text[edited.Files[0].Start:edited.Files[0].End] != "@file" || edited.Text[edited.Images[0].Start:edited.Images[0].End] != "[Image 1]" {
		t.Fatalf("binding drift: %+v", edited)
	}
	if !reflect.DeepEqual(edited.Attachments, saved.Attachments) || !reflect.DeepEqual(edited.AnswerImages, saved.AnswerImages) || edited.Files[0].Path != "private-file" || edited.Images[0].Path != "private-image" || !reflect.DeepEqual(original, saved) {
		t.Fatal("retained input changed")
	}
	if _, err := original.WithTextParts([]string{"wrong count"}); err == nil {
		t.Fatal("invalid editor accepted")
	}
}
