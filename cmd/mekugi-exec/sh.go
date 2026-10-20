package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/yusing/mekugi/internal/execsegment"
)

// Dash exposes neither its -c source nor a noninteractive startup hook. Read
// only the direct parent interpreter's launch arguments on Linux. No shell is
// selected, replaced, or started here; unsupported parents decline tracking.
func dashScript() (string, bool) {
	parent := "/proc/" + strconv.Itoa(os.Getppid())
	executable, err := os.Readlink(parent + "/exe")
	if err != nil || filepath.Base(executable) != "dash" {
		return "", false
	}
	data, err := os.ReadFile(parent + "/cmdline")
	if err != nil {
		return "", false
	}
	args := bytes.Split(data, []byte{0})
	for i := 1; i+1 < len(args); i++ {
		if string(args[i]) == "-c" || string(args[i]) == "-lc" {
			script := string(args[i+1])
			original := execsegment.ShOriginal(script)
			return original, original != script
		}
	}
	return "", false
}

func shOpen(channel, directory string) string {
	if _, ok := dashScript(); !ok {
		return ""
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return ""
	}
	id := hex.EncodeToString(random[:])
	work, _ := execsegment.ReportDirectory(directory, id)
	if err := execsegment.RequestReport(channel, id); err != nil {
		return ""
	}
	deadline := time.Now().Add(replyTimeout)
	for time.Now().Before(deadline) {
		if info, err := os.Stat(filepath.Join(work, "ack")); err == nil && info.Mode()&os.ModeNamedPipe != 0 {
			return work
		}
		time.Sleep(time.Millisecond)
	}
	return ""
}
