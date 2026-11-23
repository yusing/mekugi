package main

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"time"

	segmenttransport "github.com/yusing/mekugi/modules/commandsegments/transport"
)

// Bash identifies itself when sourcing the inline envelope. Other shells
// are supported only when /bin/sh resolves to Linux dash.
func shOpen(channel, directory, bashVersion string) string {
	if bashVersion == "" {
		executable, err := os.Readlink("/proc/" + strconv.Itoa(os.Getppid()) + "/exe")
		if err != nil || filepath.Base(executable) != "dash" {
			return ""
		}
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return ""
	}
	id := hex.EncodeToString(random[:])
	work, _ := segmenttransport.ReportDirectory(directory, id)
	if err := segmenttransport.RequestReport(channel, id); err != nil {
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
