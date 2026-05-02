package config

import "github.com/xiii/orqestra/internal/scheduler"

// BuildGraph converts the ExecutionGraphConfig from YAML into a scheduler.ExecutionGraph.
func (c *Config) BuildGraph() scheduler.ExecutionGraph {
	graph := scheduler.ExecutionGraph{
		Concurrency: c.ExecutionGraph.Concurrency,
	}
	for _, node := range c.ExecutionGraph.Agents {
		ag := scheduler.AgentNode{
			Role:             node.Role,
			ModelRef:         node.ModelRef,
			SmallModelRef:    node.SmallModelRef,
			SystemPromptFile: node.PromptFile,
			DependsOn:        node.DependsOn,
		}
		if node.Validator != nil {
			ag.Validator = &scheduler.ValidatorNode{
				Role:             node.Validator.Role,
				ModelRef:         node.Validator.ModelRef,
				SystemPromptFile: node.Validator.PromptFile,
			}
		}
		graph.Agents = append(graph.Agents, ag)
	}
	return graph
}
