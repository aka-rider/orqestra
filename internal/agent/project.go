package agent

import "fmt"

// WorkPackage is a single unit of work assigned by the Project Manager.
// Each package is executed by one worker session independently.
type WorkPackage struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Steps       []string `json:"steps"`
	Acceptance  []string `json:"acceptance"`
	DependsOn   []string `json:"depends_on,omitempty"`
	Constraints []string `json:"constraints,omitempty"`
}

// ProjectPlan is the PM's decomposition of a Specification into parallel worker tasks.
type ProjectPlan struct {
	SchemaVersion string        `json:"schema_version"`
	Packages      []WorkPackage `json:"packages"`
}

// ToSpecification converts a WorkPackage into a Specification suitable for a
// single worker session, inheriting context from the parent spec.
func (wp WorkPackage) ToSpecification(parent Specification) Specification {
	return Specification{
		SchemaVersion: parent.SchemaVersion,
		ID:            wp.ID,
		Title:         wp.Title,
		Goal:          wp.Title,
		Context:       parent.Context,
		Steps:         wp.Steps,
		Acceptance:    wp.Acceptance,
		Scope:         parent.Scope,
		Constraints:   wp.Constraints,
	}
}

// TopoWaves sorts work packages into dependency waves using Kahn's algorithm.
// Each wave contains packages whose dependencies are all in prior waves.
func TopoWaves(packages []WorkPackage) [][]WorkPackage {
	idx := make(map[string]int, len(packages))
	for i, pkg := range packages {
		idx[pkg.ID] = i
	}

	inDegree := make([]int, len(packages))
	for i := range packages {
		for range packages[i].DependsOn {
			inDegree[i]++
		}
	}

	var queue []int
	for i, d := range inDegree {
		if d == 0 {
			queue = append(queue, i)
		}
	}

	var waves [][]WorkPackage
	for len(queue) > 0 {
		wave := make([]WorkPackage, len(queue))
		for i, qi := range queue {
			wave[i] = packages[qi]
		}
		waves = append(waves, wave)

		var nextQueue []int
		for _, qi := range queue {
			curID := packages[qi].ID
			for i, pkg := range packages {
				for _, dep := range pkg.DependsOn {
					if dep == curID {
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

	return waves
}

// ValidateProjectPlan checks structural integrity of a ProjectPlan.
func ValidateProjectPlan(plan ProjectPlan) error {
	if len(plan.Packages) == 0 {
		return fmt.Errorf("project plan has no packages")
	}

	ids := make(map[string]bool, len(plan.Packages))
	for _, pkg := range plan.Packages {
		if pkg.ID == "" {
			return fmt.Errorf("work package missing id")
		}
		if ids[pkg.ID] {
			return fmt.Errorf("duplicate work package id %q", pkg.ID)
		}
		ids[pkg.ID] = true

		if len(pkg.Steps) == 0 {
			return fmt.Errorf("work package %q has no steps", pkg.ID)
		}
	}

	// Check dependency references and detect cycles.
	for _, pkg := range plan.Packages {
		for _, dep := range pkg.DependsOn {
			if !ids[dep] {
				return fmt.Errorf("work package %q depends on unknown package %q", pkg.ID, dep)
			}
			if dep == pkg.ID {
				return fmt.Errorf("work package %q depends on itself", pkg.ID)
			}
		}
	}

	if err := detectCycles(plan.Packages); err != nil {
		return err
	}

	return nil
}

// detectCycles uses Kahn's algorithm to detect cycles in the dependency graph.
func detectCycles(packages []WorkPackage) error {
	idx := make(map[string]int, len(packages))
	for i, pkg := range packages {
		idx[pkg.ID] = i
	}

	inDegree := make([]int, len(packages))
	for i, pkg := range packages {
		for _, dep := range pkg.DependsOn {
			_ = dep
			inDegree[i]++
		}
	}

	var queue []int
	for i, d := range inDegree {
		if d == 0 {
			queue = append(queue, i)
		}
	}

	processed := 0
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		processed++

		curID := packages[cur].ID
		for i, pkg := range packages {
			for _, dep := range pkg.DependsOn {
				if dep == curID {
					inDegree[i]--
					if inDegree[i] == 0 {
						queue = append(queue, i)
					}
				}
			}
		}
	}

	if processed != len(packages) {
		return fmt.Errorf("cycle detected in work package dependencies")
	}
	return nil
}
