package rundir

// Well-known artifact filenames. These names are EXACTLY the ones already on
// disk from every run written before WP15 — changing them would strand every
// historical run, so they are fixed constants, not derived.
const (
	promptFile       = "prompt.md"
	finalPlanFile    = "final_plan.md"
	workerOutputFile = "worker_output.txt"
	validationFile   = "worker_validation.txt"
)

// SavePrompt persists the original run prompt.
func (d Dir) SavePrompt(text string) error { return d.SaveArtifact(promptFile, []byte(text)) }

// LoadPrompt returns the run's original prompt, or "" if it was never written
// (older/partial runs) — see LoadArtifact for the present/absent contract.
func (d Dir) LoadPrompt() (string, error) { return d.loadOrEmpty(promptFile) }

// SaveFinalPlan persists the architect's final plan markdown. Callers on the
// fail-closed boundary (run_pipeline.go) MUST check the returned error.
func (d Dir) SaveFinalPlan(markdown string) error {
	return d.SaveArtifact(finalPlanFile, []byte(markdown))
}

// LoadFinalPlan returns the persisted plan markdown, or "" if absent.
func (d Dir) LoadFinalPlan() (string, error) { return d.loadOrEmpty(finalPlanFile) }

// SaveWorkerOutput persists the worker's harvested output/report text.
func (d Dir) SaveWorkerOutput(text string) error {
	return d.SaveArtifact(workerOutputFile, []byte(text))
}

// LoadWorkerOutput returns the worker's output text, or "" if absent.
func (d Dir) LoadWorkerOutput() (string, error) { return d.loadOrEmpty(workerOutputFile) }

// SaveValidation persists the worker self-validation's raw output text.
func (d Dir) SaveValidation(text string) error {
	return d.SaveArtifact(validationFile, []byte(text))
}

// LoadValidation returns the self-validation raw text, or "" if absent.
func (d Dir) LoadValidation() (string, error) { return d.loadOrEmpty(validationFile) }

// loadOrEmpty collapses LoadArtifact's (content, present, err) into
// (content, err), treating "absent" as "" — every one of these artifacts is
// optional display data for historical runs, never an integrity boundary.
func (d Dir) loadOrEmpty(name string) (string, error) {
	content, _, err := d.LoadArtifact(name)
	if err != nil {
		return "", err
	}
	return content, nil
}
