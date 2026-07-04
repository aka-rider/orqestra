//go:build darwin

package main

import (
	"bytes"
	"testing"
)

func TestRun_InvalidFlag(t *testing.T) {
	outStream := new(bytes.Buffer)
	errStream := new(bytes.Buffer)
	args := []string{"orqestra", "--unknown-flag"}

	exitCode := run(args, outStream, errStream)
	if exitCode != exitInvalidInput {
		t.Fatalf("expected exitInvalidInput (2), got %d. stderr: %s", exitCode, errStream.String())
	}
}

func TestRun_MissingConfig(t *testing.T) {
	outStream := new(bytes.Buffer)
	errStream := new(bytes.Buffer)
	args := []string{"orqestra", "--config", "nonexistent-config.yaml", "usage"}

	exitCode := run(args, outStream, errStream)
	if exitCode != exitUserCancelled {
		t.Fatalf("expected exitUserCancelled (130) for non-tty InitGate, got %d. stderr: %s", exitCode, errStream.String())
	}
}

func TestRun_Help(t *testing.T) {
	outStream := new(bytes.Buffer)
	errStream := new(bytes.Buffer)
	args := []string{"orqestra"}

	exitCode := run(args, outStream, errStream)
	if exitCode != exitUserCancelled {
		t.Fatalf("expected exitUserCancelled (130) for non-tty InitGate, got %d", exitCode)
	}
}
