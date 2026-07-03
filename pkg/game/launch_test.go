package game

import (
	"strings"
	"testing"
)

// The official launcher builds: neocron.exe -ticketuser "<name>" -ticket <t>
// (docs/RE_LAUNCHER.md §5.3). Args must be discrete argv elements.
func TestGameArgsMatchOfficialCmdline(t *testing.T) {
	args := gameArgs(LaunchOpts{AccountName: "Father Augusto", Ticket: "abc123"})
	want := []string{"-ticketuser", "Father Augusto", "-ticket", "abc123"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("gameArgs = %#v, want %#v", args, want)
	}
}
