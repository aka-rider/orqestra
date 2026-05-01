package tui

import (
	"fmt"
	"strings"

	"github.com/xiii/orqestra/internal/scheduler"
)

// pipelineView renders a DAG status overview for multi-agent execution.
type pipelineView struct {
	agents []pipelineAgent
	width  int
}

type pipelineAgentStatus int

const (
	pipelinePending pipelineAgentStatus = iota
	pipelineRunning
	pipelinePassed
	pipelineFailed
	pipelineWarning
)

type pipelineAgent struct {
	role      string
	status    pipelineAgentStatus
	validator *pipelineAgent
}

func newPipelineView() pipelineView {
	return pipelineView{}
}

// SetAgents initializes the pipeline view with agents from the execution graph.
func (p *pipelineView) SetAgents(graph scheduler.ExecutionGraph) {
	p.agents = make([]pipelineAgent, len(graph.Agents))
	for i, a := range graph.Agents {
		p.agents[i] = pipelineAgent{role: a.Role, status: pipelinePending}
		if a.Validator != nil {
			p.agents[i].validator = &pipelineAgent{role: a.Validator.Role, status: pipelinePending}
		}
	}
}

// HandleEvent updates agent/validator status based on scheduler events.
func (p *pipelineView) HandleEvent(event scheduler.Event) {
	switch event.Type {
	case scheduler.EventAgentStarted:
		if a := p.findAgent(event.Role); a != nil {
			a.status = pipelineRunning
		}
	case scheduler.EventAgentDone:
		if a := p.findAgent(event.Role); a != nil {
			a.status = pipelinePassed
		}
	case scheduler.EventAgentFailed:
		if a := p.findAgent(event.Role); a != nil {
			a.status = pipelineFailed
		}
	case scheduler.EventValidationStarted:
		if v := p.findValidator(event.Role); v != nil {
			v.status = pipelineRunning
		}
	case scheduler.EventValidationPassed:
		if v := p.findValidator(event.Role); v != nil {
			v.status = pipelinePassed
		}
	case scheduler.EventValidationFailed:
		if v := p.findValidator(event.Role); v != nil {
			v.status = pipelineFailed
		}
	}
}

func (p *pipelineView) findAgent(role string) *pipelineAgent {
	for i := range p.agents {
		if p.agents[i].role == role {
			return &p.agents[i]
		}
	}
	return nil
}

func (p *pipelineView) findValidator(role string) *pipelineAgent {
	for i := range p.agents {
		if p.agents[i].validator != nil && p.agents[i].validator.role == role {
			return p.agents[i].validator
		}
	}
	return nil
}

func (p pipelineView) View() string {
	if len(p.agents) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("  Pipeline Status\n")
	b.WriteString("  ───────────────\n")

	for _, a := range p.agents {
		icon := statusIcon(a.status)
		b.WriteString(fmt.Sprintf("  %s %s\n", icon, a.role))
		if a.validator != nil {
			vIcon := statusIcon(a.validator.status)
			b.WriteString(fmt.Sprintf("    └─ %s %s\n", vIcon, a.validator.role))
		}
	}

	return b.String()
}

func statusIcon(s pipelineAgentStatus) string {
	switch s {
	case pipelinePending:
		return "○"
	case pipelineRunning:
		return "⟳"
	case pipelinePassed:
		return "✓"
	case pipelineFailed:
		return "✗"
	case pipelineWarning:
		return "⚠"
	default:
		return "?"
	}
}
