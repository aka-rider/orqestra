package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/xiii/orqestra/internal/agent"
)

// applyRestartSkip sets up the session for a restart run:
// it copies completed phase directories from the source run into the new session,
// then seeds plan content from the new session's phase directories.
func applyRestartSkip(ctx context.Context, src, dst string, session agent.SessionDir,
	input Input, setup PipelineSetup,
) (finalPlanMarkdown, planSessionID, draftMarkdownForPlanning, criticReportMarkdown string, planSessionIDValid bool) {
	// Copy completed phase directories from source to new session.
	phaseDirs := []struct {
		name     string
		required bool
	}{
		{name: "research", required: setup.Research},
		{name: "deliberation", required: setup.Research || setup.Execution || setup.Validation},
		{name: "execution", required: setup.Execution},
		{name: "validation", required: setup.Validation},
	}

	for _, pd := range phaseDirs {
		srcDir := filepath.Join(src, pd.name)
		dstDir := filepath.Join(dst, pd.name)
		if _, err := os.Stat(srcDir); os.IsNotExist(err) {
			continue
		}
		if err := copyDirRecursive(srcDir, dstDir); err != nil {
			slog.Warn("restart: copy phase dir failed", "phase", pd.name, "err", err)
			continue
		}
	}

	// Seed from the new session's phase directories.
	// For plan gates, find the highest plan-vN.md in the deliberation dir.
	delDir := session.DeliberationDir()
	if highestPlan := findHighestPlan(delDir); highestPlan != "" {
		data, err := os.ReadFile(highestPlan)
		if err == nil {
			finalPlanMarkdown = string(data)
			planSessionIDValid = true
		}
	}

	// For research, find the highest plan-vN.md in the research dir.
	resDir := session.ResearchDir()
	if highestPlan := findHighestPlan(resDir); highestPlan != "" {
		data, err := os.ReadFile(highestPlan)
		if err == nil {
			draftMarkdownForPlanning = string(data)
		}
	}

	// Load planSessionID from architect meta if available.
	archMetaPath := filepath.Join(dst, "architect_meta.json")
	if data, err := os.ReadFile(archMetaPath); err == nil {
		var meta agent.StepMeta
		if json.Unmarshal(data, &meta) == nil && meta.ClaudeSessionID != "" {
			planSessionID = meta.ClaudeSessionID
			planSessionIDValid = true
		}
	}

	// Load critic report if available.
	criticReportMarkdown = restartReadStringArtifact(dst, "critic_report.md")

	return finalPlanMarkdown, planSessionID, draftMarkdownForPlanning, criticReportMarkdown, planSessionIDValid
}

// copyDirRecursive recursively copies a directory from src to dst.
func copyDirRecursive(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("reading %s: %w", src, err)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dst, err)
	}
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := copyDirRecursive(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			data, err := os.ReadFile(srcPath)
			if err != nil {
				return fmt.Errorf("reading %s: %w", srcPath, err)
			}
			if err := os.WriteFile(dstPath, data, 0o644); err != nil {
				return fmt.Errorf("writing %s: %w", dstPath, err)
			}
		}
	}
	return nil
}

// copyCompletedArtifacts copies artifacts from a previous run into a new session
// directory so that completed phases can be skipped on restart.
// It copies phase directories (research/, deliberation/, execution/, validation/)
// instead of the old flat file list.
func copyCompletedArtifacts(src, dst string) error {
	if src == "" || dst == "" {
		return nil
	}

	// Copy phase directories: research/, deliberation/, execution/, validation/.
	phaseDirs := []string{"research", "deliberation", "execution", "validation"}
	for _, name := range phaseDirs {
		srcDir := filepath.Join(src, name)
		dstDir := filepath.Join(dst, name)
		if _, err := os.Stat(srcDir); os.IsNotExist(err) {
			continue
		}
		if err := copyDirRecursive(srcDir, dstDir); err != nil {
			return fmt.Errorf("copying phase dir %s: %w", name, err)
		}
	}

	// Also copy root-level artifacts for backward compatibility.
	rootArtifacts := []string{
		"prompt.md",
		"final_plan.md",
		"researcher_draft.md",
		"researcher_meta.json",
		"researcher_session.jsonl",
		"architect_meta.json",
		"architect_session.jsonl",
		"critic_report.md",
		"critic_meta.json",
		"critic_session.jsonl",
		"worker_output.txt",
		"worker_meta.json",
		"worker_session.jsonl",
		"validation.txt",
		"validator_meta.json",
		"validator_session.jsonl",
	}
	for _, name := range rootArtifacts {
		srcPath := filepath.Join(src, name)
		if !restartFileExists(srcPath) {
			continue
		}
		dstPath := filepath.Join(dst, name)
		if err := copyDir(srcPath, dstPath); err != nil {
			return fmt.Errorf("copying artifact %s: %w", name, err)
		}
	}
	return nil
}
