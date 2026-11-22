package rollout

import (
	"encoding/json/jsontext"
	"testing"
)

func TestReplayLegacyAsyncQuestions(t *testing.T) {
	item, supported, err := replayItem(jsontext.Value(`{"type":"AgentMessage","id":"message","delivery":"async","questions":[{"title":"Continue?","options":["Yes","No"]},{"id":"authored-id","title":"Why?"}]}`))
	if err != nil || !supported {
		t.Fatalf("legacy replay: supported=%v err=%v", supported, err)
	}
	if item.Questions[0].Question != "Continue?" || item.Questions[0].Options[0].Label != "Yes" || item.Questions[1].ID != "authored-id" {
		t.Fatal(item.Questions)
	}
}
