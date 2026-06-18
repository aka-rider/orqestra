package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xiii/orqestra/internal/harness"
)

type recordingExecutor struct {
	ctxDeadline time.Time
	hasDeadline bool
	result      harness.RunResult
	err         error
}

func (r *recordingExecutor) Run(ctx context.Context, _ harness.ProcessSpec, _ <-chan harness.Message, _ harness.Sink) (harness.RunResult, error) {
	r.ctxDeadline, r.hasDeadline = ctx.Deadline()
	return r.result, r.err
}

func TestWatchdog_SetsTimeout(t *testing.T) {
	inner := &recordingExecutor{}
	w := NewWatchdogExecutor(inner)

	spec := harness.ProcessSpec{Timeout: 10 * time.Minute}
	before := time.Now()
	_, _ = w.Run(context.Background(), spec, nil, nil)

	if !inner.hasDeadline {
		t.Fatal("expected deadline to be set")
	}
	expected := before.Add(10 * time.Minute)
	if inner.ctxDeadline.Before(before) || inner.ctxDeadline.After(expected.Add(time.Second)) {
		t.Errorf("deadline %v not within expected window [%v, %v]", inner.ctxDeadline, before, expected)
	}
}

func TestWatchdog_NoTimeout(t *testing.T) {
	inner := &recordingExecutor{}
	w := NewWatchdogExecutor(inner)

	spec := harness.ProcessSpec{Timeout: 0}
	_, _ = w.Run(context.Background(), spec, nil, nil)

	if inner.hasDeadline {
		t.Error("expected no deadline when timeout is zero")
	}
}

func TestWatchdog_RespectsParentCancel(t *testing.T) {
	inner := &recordingExecutor{err: context.Canceled}
	w := NewWatchdogExecutor(inner)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	spec := harness.ProcessSpec{Timeout: 30 * time.Second}
	_, err := w.Run(ctx, spec, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestWatchdog_PropagatesError(t *testing.T) {
	wantErr := errors.New("inner failure")
	inner := &recordingExecutor{err: wantErr}
	w := NewWatchdogExecutor(inner)

	_, err := w.Run(context.Background(), harness.ProcessSpec{Timeout: time.Minute}, nil, nil)
	if !errors.Is(err, wantErr) {
		t.Errorf("expected %v, got %v", wantErr, err)
	}
}
