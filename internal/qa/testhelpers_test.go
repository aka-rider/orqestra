package qa

import (
	"context"
	"io"

	"github.com/xiii/orqestra/internal/harness"
)

// mockCLIRunner is a test double for harness.CLIRunner.
type mockCLIRunner struct {
	response  string
	err       error
	callCount int
}

func (m *mockCLIRunner) RunPrint(_ context.Context, _, _ string) (harness.RunResult, error) {
	m.callCount++
	if m.err != nil {
		return harness.RunResult{}, m.err
	}
	return harness.RunResult{Output: m.response}, nil
}

func (m *mockCLIRunner) RunStreaming(_ context.Context, _, _ string, _ io.Writer) (harness.RunResult, error) {
	m.callCount++
	if m.err != nil {
		return harness.RunResult{}, m.err
	}
	return harness.RunResult{Output: m.response}, nil
}
