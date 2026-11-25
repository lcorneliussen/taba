package wtc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"taba.sh/taba/internal/forgecli"
	"taba.sh/taba/internal/prompts"
)

func collectReviewChecks(m ReviewManifest) headCheckEvidence {
	evidence := unknownHeadChecks(m.HeadSHA)
	if m.PR == "" || !validCheckHead(m.HeadSHA) {
		return evidence
	}
	if m.Forge == "bitbucket" {
		raw, _, err := statusWithForgeBudget("bitbucket.org", nil, func() ([]byte, error) {
			head, err := reviewPRHead(m)
			return []byte(head), err
		})
		if err != nil || !SameReviewSHA(m.HeadSHA, string(raw)) {
			return evidence
		}
		return statusBBHeadChecks(m.Slug, m.HeadSHA, true)
	}
	if m.Forge != "github" {
		return evidence
	}
	raw, _, err := statusWithForgeBudget("github.com", nil, func() ([]byte, error) {
		return forgecli.Command("gh", "pr", "view", m.PR, "--repo", m.Slug, "--json", "headRefOid,statusCheckRollup").Output()
	})
	if err != nil {
		return evidence
	}
	var pr struct {
		Head   string `json:"headRefOid"`
		Checks []struct {
			Conclusion string `json:"conclusion"`
			State      string `json:"state"`
			Status     string `json:"status"`
		} `json:"statusCheckRollup"`
	}
	if json.Unmarshal(raw, &pr) != nil || pr.Head != m.HeadSHA || pr.Checks == nil {
		return evidence
	}
	evidence.Result, _ = statusCheckResult(pr.Checks)
	evidence.Complete = true
	return evidence
}

func writeReviewChecks(bundle string, m ReviewManifest) (headCheckEvidence, error) {
	evidence := collectReviewChecks(m)
	raw, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return evidence, err
	}
	return evidence, os.WriteFile(filepath.Join(bundle, "checks.json"), append(raw, '\n'), 0644)
}

func reviewCheckPrompt(template string, suffix prompts.Text) string {
	return strings.TrimRight(template, "\n") + "\n\n" + suffix.String() + "\n"
}

func reviewChecksReport(bundle string, m ReviewManifest) string {
	var evidence headCheckEvidence
	raw, err := os.ReadFile(filepath.Join(bundle, "checks.json"))
	if err != nil || json.Unmarshal(raw, &evidence) != nil || evidence.Head != m.HeadSHA {
		evidence = unknownHeadChecks(m.HeadSHA)
	}
	return evidence.report()
}
