package claude

import (
	"testing"
)

func TestNativeShellCarriersAndHistory(t *testing.T) {
	var a adapter
	start, err := a.decode([]byte(`{"kind":"history","event":{"type":"user","uuid":"native-command","session_id":"saved","message":{"content":"<bash-input>printf '&amp;&lt;世界&gt;' &gt; result</bash-input>"}}}`))
	if err != nil || len(start) != 1 || start[0].Shell == nil || start[0].Shell.Command != "printf '&<世界>' > result" || !start[0].Historical {
		t.Fatalf("native shell input: %+v %v", start, err)
	}
	end, err := a.decode([]byte(`{"kind":"history","event":{"type":"user","uuid":"native-output","session_id":"saved","message":{"content":"<bash-stdout>&amp;&lt;/bash-stdout&gt;世界</bash-stdout><bash-stderr>failure</bash-stderr><bash-exit-code>7</bash-exit-code>"}}}`))
	if err != nil || len(end) != 1 || end[0].Shell == nil || end[0].Shell.ID != start[0].Shell.ID || end[0].Shell.Output != "&</bash-stdout>世界failure" || end[0].Shell.ExitCode == nil || *end[0].Shell.ExitCode != 7 || !end[0].Historical {
		t.Fatalf("native shell output: %+v %v", end, err)
	}
	if _, code := shellOutput("<bash-stdout>partial</bash-stdout>"); code != nil {
		t.Fatal("partial native output fabricated an exit")
	}
	merged, err := a.decode([]byte(`{"kind":"history","event":{"type":"user","uuid":"coalesced-output","session_id":"saved","message":{"content":"<bash-input>printf 世界</bash-input>\n<bash-stdout>世界</bash-stdout><bash-stderr></bash-stderr><bash-exit-code>0</bash-exit-code>"}}}`))
	if err != nil || len(merged) != 2 || merged[1].Shell == nil || merged[1].Shell.ID != "history/coalesced-output" || merged[1].Shell.Command != "printf 世界" || merged[1].Shell.Output != "世界" || !merged[1].Historical {
		t.Fatalf("native coalesced history: %+v %v", merged, err)
	}
}
