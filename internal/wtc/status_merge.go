package wtc

import (
	"strings"

	"taba.sh/taba/internal/next"
)

// statusPredictBBMerge supplements Bitbucket's unknown mergeability with a
// local prediction for the exact commit pair reported by the forge. It never
// fetches missing objects or substitutes HEAD or a tracking branch for them.
func statusPredictBBMerge(detail statusPRDetail, forge, slug string, rows []StatusRepo) statusPRDetail {
	if forge != "bitbucket.org" || (detail.State != "OPEN" && detail.State != "DRAFT") ||
		(detail.Merge != "" && detail.Merge != "UNKNOWN") {
		return detail
	}
	for _, row := range rows {
		if row.Forge != forge || row.Slug != slug {
			continue
		}
		head, headOK := statusResolveCommitID(row.Worktree, detail.HeadCommit)
		base, baseOK := statusResolveCommitID(row.Worktree, detail.BaseCommit)
		if !headOK || !baseOK {
			continue
		}
		conflicts, ok := next.PredictMergeAt(row.Worktree, head, base)
		if !ok {
			continue
		}
		detail.MergeSource = "local"
		detail.MergeConflicts = conflicts
		detail.HeadCommit, detail.BaseCommit = head, base
		switch {
		case len(conflicts) > 0:
			detail.Merge = "CONFLICTING"
		case catchUpGitOK(row.Worktree, "merge-base", "--is-ancestor", detail.BaseCommit, detail.HeadCommit):
			detail.Merge = "CLEAN"
		default:
			detail.Merge = "BEHIND"
		}
		return detail
	}
	return detail
}

// Bitbucket may abbreviate commit hashes. Resolve only unique object prefixes,
// never similarly named refs, and record the full commit identity in status.
func statusResolveCommitID(dir, value string) (string, bool) {
	if len(value) < 7 || len(value) > 64 {
		return "", false
	}
	for _, ch := range value {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return "", false
		}
	}
	objects, err := statusGit(dir, "rev-parse", "--disambiguate="+value)
	if err != nil || strings.ContainsAny(objects, "\n\r") || !strings.HasPrefix(objects, value) || (len(objects) != 40 && len(objects) != 64) {
		return "", false
	}
	kind, err := statusGit(dir, "cat-file", "-t", objects)
	return objects, err == nil && kind == "commit"
}

func statusMergeProvenance(detail statusPRDetail) (head, base string) {
	if detail.MergeSource == "local" {
		return detail.HeadCommit, detail.BaseCommit
	}
	return "", ""
}
