package scheduler

import (
	"context"
	"fmt"
	"sync"

	"golang.org/x/sync/semaphore"
)

// AgentRunner is the callback that executes a single agent's work.
// The spec parameter is an opaque value passed through from Run.
type AgentRunner func(ctx context.Context, node AgentNode, spec any) error

// Scheduler orchestrates agent execution based on an ExecutionGraph DAG.
type Scheduler struct {
	graph ExecutionGraph
	order [][]int // topological waves: each wave is a slice of agent indices
}

// New validates the graph (cycle detection) and returns a Scheduler.
func New(graph ExecutionGraph) (*Scheduler, error) {
	order, err := topoSort(graph)
	if err != nil {
		return nil, err
	}
	return &Scheduler{graph: graph, order: order}, nil
}

// Run executes the graph using the provided runner callback.
// It emits events via the notify function as agents progress.
func (s *Scheduler) Run(ctx context.Context, spec any, runner AgentRunner, notify func(Event)) error {
	if notify == nil {
		notify = func(Event) {}
	}

	// Determine concurrency limit
	var sem *semaphore.Weighted
	conc := s.graph.Concurrency
	if conc > 0 {
		sem = semaphore.NewWeighted(int64(conc))
	}

	// Track agent status
	type agentStatus int
	const (
		statusPending agentStatus = iota
		statusDone
		statusFailed
	)
	status := make([]agentStatus, len(s.graph.Agents))

	// Execute waves in topological order
	for _, wave := range s.order {
		var wg sync.WaitGroup
		for _, idx := range wave {
			// Check if all dependencies are satisfied
			node := s.graph.Agents[idx]
			canRun := true
			for _, dep := range node.DependsOn {
				depIdx := s.roleIndex(dep)
				if depIdx < 0 || status[depIdx] != statusDone {
					canRun = false
					break
				}
			}
			if !canRun {
				status[idx] = statusFailed
				notify(Event{Type: EventAgentFailed, Role: node.Role, Message: "dependency not satisfied"})
				continue
			}

			wg.Add(1)
			agentIdx := idx
			agentNode := node
			go func() {
				defer wg.Done()

				// Acquire semaphore if bounded
				if sem != nil {
					if err := sem.Acquire(ctx, 1); err != nil {
						status[agentIdx] = statusFailed
						notify(Event{Type: EventAgentFailed, Role: agentNode.Role, Err: err})
						return
					}
					defer sem.Release(1)
				}

				// Run agent
				notify(Event{Type: EventAgentStarted, Role: agentNode.Role})
				err := runner(ctx, agentNode, spec)
				if err != nil {
					status[agentIdx] = statusFailed
					notify(Event{Type: EventAgentFailed, Role: agentNode.Role, Err: err})
					return
				}

				// Run validator if present (stub: always pass)
				if agentNode.Validator != nil {
					notify(Event{Type: EventValidationStarted, Role: agentNode.Validator.Role})
					// Stub: validation always passes
					notify(Event{Type: EventValidationPassed, Role: agentNode.Validator.Role})
				}

				status[agentIdx] = statusDone
				notify(Event{Type: EventAgentDone, Role: agentNode.Role})
			}()
		}
		wg.Wait()

		// Check context cancellation between waves
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}

	return nil
}

// roleIndex returns the index of the agent with the given role, or -1.
func (s *Scheduler) roleIndex(role string) int {
	for i, a := range s.graph.Agents {
		if a.Role == role {
			return i
		}
	}
	return -1
}

// topoSort performs topological sorting using Kahn's algorithm.
// Returns waves (layers) of agent indices that can run in parallel.
// Returns an error if cycles are detected.
func topoSort(graph ExecutionGraph) ([][]int, error) {
	n := len(graph.Agents)
	if n == 0 {
		return nil, nil
	}

	// Build role -> index map
	roleIdx := make(map[string]int, n)
	for i, a := range graph.Agents {
		roleIdx[a.Role] = i
	}

	// Compute in-degree
	inDegree := make([]int, n)
	for i, a := range graph.Agents {
		for _, dep := range a.DependsOn {
			if _, ok := roleIdx[dep]; ok {
				inDegree[i]++
			}
		}
	}

	// Initialize queue with zero in-degree nodes
	var queue []int
	for i, d := range inDegree {
		if d == 0 {
			queue = append(queue, i)
		}
	}

	var waves [][]int
	processed := 0

	for len(queue) > 0 {
		// Current wave: all nodes with in-degree 0
		wave := make([]int, len(queue))
		copy(wave, queue)
		waves = append(waves, wave)
		processed += len(wave)

		var nextQueue []int
		for _, idx := range wave {
			role := graph.Agents[idx].Role
			// Reduce in-degree for dependents
			for i, a := range graph.Agents {
				for _, dep := range a.DependsOn {
					if dep == role {
						inDegree[i]--
						if inDegree[i] == 0 {
							nextQueue = append(nextQueue, i)
						}
					}
				}
			}
		}
		queue = nextQueue
	}

	if processed != n {
		return nil, fmt.Errorf("cycle detected in execution graph: processed %d of %d agents", processed, n)
	}

	return waves, nil
}
