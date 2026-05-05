package agent

import (
	"testing"
)

func TestScanForBEL_StandaloneBEL(t *testing.T) {
	r := &Runner{}
	var belCount int
	onBEL := func() { belCount++ }

	// Standalone BEL at start.
	r.scanForBEL([]byte{0x07, 'h', 'e', 'l', 'l', 'o'}, onBEL)
	if belCount != 1 {
		t.Fatalf("expected 1 BEL, got %d", belCount)
	}

	// Reset.
	belCount = 0

	// Multiple standalone BELs.
	r.scanForBEL([]byte{0x07, 'a', 0x07, 'b', 0x07}, onBEL)
	if belCount != 3 {
		t.Fatalf("expected 3 BELs, got %d", belCount)
	}
}

func TestScanForBEL_OSCSequenceBELIgnored(t *testing.T) {
	r := &Runner{}
	var belCount int
	onBEL := func() { belCount++ }

	// OSC sequence terminated by BEL: \x1b]0;title\x07
	data := []byte{0x1b, ']', '0', ';', 't', 'i', 't', 'l', 'e', 0x07}
	r.scanForBEL(data, onBEL)
	if belCount != 0 {
		t.Fatalf("expected 0 BELs (OSC terminator should be ignored), got %d", belCount)
	}
}

func TestScanForBEL_MixedStandaloneAndOSC(t *testing.T) {
	r := &Runner{}
	var belCount int
	onBEL := func() { belCount++ }

	// Standalone BEL, then OSC with BEL terminator, then standalone BEL.
	data := []byte{0x07, 0x1b, ']', '2', ';', 'x', 0x07, 0x07}
	r.scanForBEL(data, onBEL)
	if belCount != 2 {
		t.Fatalf("expected 2 standalone BELs, got %d", belCount)
	}
}

func TestBuildAgentCommand_NonInteractive(t *testing.T) {
	cmd := buildAgentCommand("do something", "/path/to/prompt.md", false)
	if cmd[0] != "claude" {
		t.Fatal("expected claude binary")
	}
	found := false
	for _, arg := range cmd {
		if arg == "--append-system-prompt-file" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected --append-system-prompt-file in non-interactive command")
	}
}

func TestBuildAgentCommand_Interactive(t *testing.T) {
	cmd := buildAgentCommand("ignored", "/path/to/prompt.md", true)
	for _, arg := range cmd {
		if arg == "-p" {
			t.Fatal("interactive mode should not use -p flag")
		}
	}
}

func TestSessionDir_WriteAndRead(t *testing.T) {
	dir := t.TempDir()
	sd := SessionDir{Path: dir}

	data := []byte(`{"test": true}`)
	if err := sd.WriteArtifact("test.json", data); err != nil {
		t.Fatal(err)
	}

	got, err := sd.ReadArtifact("test.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(data) {
		t.Fatalf("expected %q, got %q", data, got)
	}
}
