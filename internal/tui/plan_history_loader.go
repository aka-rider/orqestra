package tui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/xiii/orqestra/internal/plan"
)

// loadPlanRevisions returns a tea.Cmd that opens the plan-history micro-repo,
// reads the revision list, resolves HEAD, and produces planRevisionsLoadedMsg.
// Failures populate planRevisionsLoadedMsg.Err. The HEAD SHA is resolved here
// (not by the caller) so the read-only entry point — which has no a-priori
// HEAD reference — can still detect "selected is HEAD".
func loadPlanRevisions(historyDir string) tea.Cmd {
	return func() tea.Msg {
		repo, err := plan.OpenGitRepo(historyDir)
		if err != nil {
			return planRevisionsLoadedMsg{HistoryDir: historyDir, Err: err}
		}
		revs, err := repo.Revisions()
		if err != nil {
			return planRevisionsLoadedMsg{HistoryDir: historyDir, Err: err}
		}
		headSHA, headErr := repo.HeadCommitHash()
		if headErr != nil {
			// HEAD lookup is best-effort: the viewer renders without the
			// "(HEAD)" label rather than failing the entire load.
			headSHA = ""
		}
		return planRevisionsLoadedMsg{
			HistoryDir: historyDir,
			HeadSHA:    headSHA,
			Revisions:  revs,
		}
	}
}

// loadPlanRevisionDetail returns a tea.Cmd that fetches the content and the
// diff vs HEAD for a single revision. When sha == headSHA the diff is skipped
// and returned empty.
func loadPlanRevisionDetail(historyDir, sha, headSHA string) tea.Cmd {
	return func() tea.Msg {
		repo, err := plan.OpenGitRepo(historyDir)
		if err != nil {
			return planRevisionDetailLoadedMsg{SHA: sha, Err: err}
		}
		content, err := repo.ContentAt(sha)
		if err != nil {
			return planRevisionDetailLoadedMsg{SHA: sha, Err: err}
		}
		if sha == headSHA || headSHA == "" {
			return planRevisionDetailLoadedMsg{SHA: sha, Content: content}
		}
		diff, err := repo.DiffBetween(sha, headSHA)
		if err != nil {
			return planRevisionDetailLoadedMsg{SHA: sha, Content: content, Err: err}
		}
		return planRevisionDetailLoadedMsg{SHA: sha, Content: content, Diff: diff}
	}
}
