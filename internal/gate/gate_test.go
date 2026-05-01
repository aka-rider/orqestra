package gate_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/xiii/orqestra/internal/gate"
	"github.com/xiii/orqestra/internal/types"
)

func TestConfirm_Approved(t *testing.T) {
	input := strings.NewReader("y\n")
	output := &bytes.Buffer{}

	g := gate.New(input, output)
	spec := types.Specification{
		Goal:       "Build a thing",
		Steps:      []string{"Step 1", "Step 2"},
		Acceptance: []string{"It works"},
	}

	approved, err := g.Confirm(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !approved {
		t.Fatal("expected approved")
	}

	out := output.String()
	if !strings.Contains(out, "Build a thing") {
		t.Error("output should contain the goal")
	}
	if !strings.Contains(out, "Step 1") {
		t.Error("output should contain steps")
	}
}

func TestConfirm_Rejected(t *testing.T) {
	input := strings.NewReader("n\n")
	output := &bytes.Buffer{}

	g := gate.New(input, output)
	spec := types.Specification{
		Goal:       "Something",
		Steps:      []string{"Do it"},
		Acceptance: []string{"Done"},
	}

	approved, err := g.Confirm(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if approved {
		t.Fatal("expected rejected")
	}
}

func TestConfirm_EmptyInput(t *testing.T) {
	input := strings.NewReader("\n")
	output := &bytes.Buffer{}

	g := gate.New(input, output)
	spec := types.Specification{
		Goal:       "X",
		Steps:      []string{"Y"},
		Acceptance: []string{"Z"},
	}

	approved, err := g.Confirm(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if approved {
		t.Fatal("empty input should default to reject")
	}
}
