package agent

import (
	"context"
	"io"

	"github.com/xiii/orqestra/internal/harness"
)

// qaMockCLIRunner is a test double for harness.CLIRunner in QA tests.
type qaMockCLIRunner struct {
	response  string
	err       error
	callCount int
}

func (m *qaMockCLIRunner) RunPrint(_ context.Context, _, _ string) (harness.RunResult, error) {
	m.callCount++
	if m.err != nil {
		return harness.RunResult{}, m.err
	}
	return harness.RunResult{Output: m.response}, nil
}

func (m *qaMockCLIRunner) RunStreaming(_ context.Context, _, _ string, _ io.Writer) (harness.RunResult, error) {
	m.callCount++
	if m.err != nil {
		return harness.RunResult{}, m.err
	}
	return harness.RunResult{Output: m.response}, nil
}
