package llm

import (
	"context"
	"time"
)

// MockProvider is a test double for the Provider interface.
type MockProvider struct {
	IDValue      string
	Response     string
	Err          error
	CallCount    int
	LastRequest  *Request
}

func (m *MockProvider) ID() string {
	return m.IDValue
}

func (m *MockProvider) Generate(_ context.Context, req *Request) (*Response, error) {
	m.CallCount++
	m.LastRequest = req
	if m.Err != nil {
		return nil, m.Err
	}
	return &Response{
		Content: m.Response,
		Model:   req.Model,
		Latency: time.Millisecond,
	}, nil
}
