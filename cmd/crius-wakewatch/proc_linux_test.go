//go:build linux

package main

import (
	"os"
	"strings"
	"testing"
)

// On Linux the process reads say they are not supported, so the paid-usage check lists a live claude with an error
// instead of passing it with no environment.
func TestLinuxProcessReadsAreLoud(t *testing.T) {
	self := os.Getpid()
	if _, _, err := procArgs(self); err == nil || !strings.Contains(err.Error(), "not supported on Linux") {
		t.Errorf("procArgs: %v, want not supported", err)
	}
	if _, ok, err := process(self); ok || err == nil || !strings.Contains(err.Error(), "not supported on Linux") {
		t.Errorf("process: %v %v, want not supported", ok, err)
	}
	if ended(self) {
		t.Error("ended: our own process reads as ended")
	}
}
