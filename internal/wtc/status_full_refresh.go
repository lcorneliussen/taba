package wtc

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"taba.sh/taba/internal/envcompat"
)

var statusAuditNote func(string)

func statusFullRefreshEvery() time.Duration {
	raw := strings.TrimSpace(envcompat.Getenv("WTC_FORGE_FULL_REFRESH"))
	if raw == "" {
		return 10 * time.Minute
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func statusFullRefreshDue(forge, slug, key string) bool {
	every := statusFullRefreshEvery()
	if every <= 0 {
		return false
	}
	info, err := os.Lstat(statusForgeEntryPath("full", forge, slug, key))
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return true
	}
	return time.Since(info.ModTime()) >= every
}

func statusMarkFullRefresh(forge, slug, key string) {
	statusWriteForgeEntry("full", forge, slug, key, []byte("ok"))
}

func statusAuditValue(value string) string {
	value = strings.ReplaceAll(value, "\n", " ")
	if len(value) > 80 {
		value = value[:80]
	}
	return value
}

func statusAuditDiff(name, from, to string, parts []string) []string {
	if from == to {
		return parts
	}
	return append(parts, name+":"+statusAuditValue(from)+"->"+statusAuditValue(to))
}

func statusPRDetailDiff(before, after statusPRDetail) string {
	var parts []string
	parts = statusAuditDiff("state", before.State, after.State, parts)
	parts = statusAuditDiff("checks", before.Checks, after.Checks, parts)
	parts = statusAuditDiff("checks-unsettled", strconv.FormatBool(before.ChecksUnsettled), strconv.FormatBool(after.ChecksUnsettled), parts)
	parts = statusAuditDiff("merge", before.Merge, after.Merge, parts)
	parts = statusAuditDiff("review", before.Review, after.Review, parts)
	parts = statusAuditDiff("title", before.Title, after.Title, parts)
	parts = statusAuditDiff("base", before.Base, after.Base, parts)
	parts = statusAuditDiff("merged", before.MergedOn, after.MergedOn, parts)
	parts = statusAuditDiff("commit", before.MergeCommit, after.MergeCommit, parts)
	return strings.Join(parts, " ")
}

func statusBuildValue(build *StatusBuild) (number, checks, sha string) {
	if build == nil {
		return "", "", ""
	}
	if build.Build != nil {
		number = *build.Build
	}
	if build.Checks != nil {
		checks = *build.Checks
	}
	if build.SHA != nil {
		sha = *build.SHA
	}
	return number, checks, sha
}

func statusBuildDiff(before, after *StatusBuild) string {
	fromNumber, fromChecks, fromSHA := statusBuildValue(before)
	toNumber, toChecks, toSHA := statusBuildValue(after)
	var parts []string
	parts = statusAuditDiff("build", fromNumber, toNumber, parts)
	parts = statusAuditDiff("checks", fromChecks, toChecks, parts)
	parts = statusAuditDiff("sha", fromSHA, toSHA, parts)
	return strings.Join(parts, " ")
}

func statusLogIncrementalMiss(forge, slug, key, source, diff string) {
	if strings.TrimSpace(diff) == "" {
		return
	}
	line := fmt.Sprintf("incremental miss %s %s %s %s %s", forge, slug, key, source, diff)
	if statusAuditNote != nil {
		statusAuditNote(line)
	}
	dir := statusForgeCacheDir()
	if !statusSecureCacheDir(dir) {
		return
	}
	file, err := os.OpenFile(filepath.Join(dir, "incremental-misses.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer file.Close()
	fmt.Fprintf(file, "%s %s\n", time.Now().UTC().Format(time.RFC3339), line)
}

func statusIncrementalMissLog() string {
	return filepath.Join(statusForgeCacheDir(), "incremental-misses.log")
}

func statusFetchPRRaw(record PRRecord, slug, forge string) ([]byte, error) {
	if statusForgeCooling(forge) {
		return nil, errForgeCooling
	}
	if !statusTakeForgeToken(forge) {
		return nil, errForgeBudget
	}
	var raw []byte
	var err error
	switch forge {
	case "github.com":
		raw, err = statusJSON("gh", "pr", "view", record.Number, "--repo", slug, "--json", "number,state,title,isDraft,statusCheckRollup,reviewDecision,mergeStateStatus,mergeable,reviewRequests,latestReviews,mergedAt,baseRefName,mergeCommit")
	case "bitbucket.org":
		parts := strings.SplitN(slug, "/", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid Bitbucket repository slug")
		}
		raw, err = statusJSON("bb", "pr", "view", record.Number, "--workspace", parts[0], "--repo", parts[1], "--json")
	default:
		return nil, fmt.Errorf("unsupported forge")
	}
	if err != nil && statusRateLimited(err) {
		statusForgeCool(forge, statusRetryAfter(err))
	}
	return raw, err
}

func statusConfirmFullPR(record PRRecord, slug, forge string, incremental statusPRDetail, source string) statusPRDetail {
	unlock, lockErr := statusLockCache(statusForgeEntryPath("pr", forge, slug, record.Number))
	if lockErr == nil {
		defer unlock()
	}
	return statusConfirmFullPRLocked(record, slug, forge, incremental, source)
}

func statusConfirmFullPRLocked(record PRRecord, slug, forge string, incremental statusPRDetail, source string) statusPRDetail {
	if !statusFullRefreshDue(forge, slug, record.Number) {
		if cached, ok := statusReadCachedDetail(record, slug, forge); ok {
			return cached
		}
		return incremental
	}
	started := time.Now()
	raw, err := statusFetchPRRaw(record, slug, forge)
	if err != nil || len(raw) == 0 {
		return incremental
	}
	full, parseErr := statusParseForgeDetail(forge, raw, record)
	if parseErr != nil {
		return incremental
	}
	if forge == "bitbucket.org" {
		full = statusBBCompleteMergeTime(full, slug)
	}
	statusWriteForgeEntrySince("pr", forge, slug, record.Number, raw, started)
	statusMarkFullRefresh(forge, slug, record.Number)
	statusLogIncrementalMiss(forge, slug, "#"+record.Number, source, statusPRDetailDiff(incremental, full))
	return full
}

func statusConfirmFullBuild(slug, branch string, incremental *StatusBuild, source, user, pass string) *StatusBuild {
	unlock, lockErr := statusLockCache(statusForgeEntryPath("build", "bitbucket.org", slug, branch))
	if lockErr == nil {
		defer unlock()
	}
	corrected, found := statusAuditedCorrection(slug, branch, incremental)
	if !statusFullRefreshDue("bitbucket.org", slug, "build:"+branch) {
		if found {
			return corrected
		}
		return incremental
	}
	fallback := incremental
	if found {
		fallback = corrected
	}
	parts := strings.SplitN(slug, "/", 2)
	if len(parts) != 2 {
		return fallback
	}
	if statusForgeCooling("bitbucket.org") || !statusTakeForgeToken("bitbucket.org") {
		return fallback
	}
	started := time.Now()
	raw, err := statusBBPipelines(parts[0], parts[1], branch, user, pass)
	if err != nil {
		if statusRateLimited(err) {
			statusForgeCool("bitbucket.org", statusRetryAfter(err))
		}
		return fallback
	}
	full, parseErr := statusParseBBBuild(raw, slug, branch)
	if parseErr != nil {
		return fallback
	}
	statusWriteForgeEntrySince("build", "bitbucket.org", slug, branch, raw, started)
	statusWriteForgeEntry("full", "bitbucket.org", slug, "build:"+branch, []byte(statusBuildFingerprint(incremental)))
	statusLogIncrementalMiss("bitbucket.org", slug, branch, source, statusBuildDiff(incremental, full))
	return full
}

func statusBuildFingerprint(build *StatusBuild) string {
	number, checks, sha := statusBuildValue(build)
	return number + "|" + checks + "|" + sha
}

func statusPreferAuditedBuild(slug, branch string, pageBuild *StatusBuild) (*StatusBuild, bool) {
	if pageBuild == nil || statusFullRefreshDue("bitbucket.org", slug, "build:"+branch) {
		return nil, false
	}
	return statusAuditedCorrection(slug, branch, pageBuild)
}

func statusAuditedCorrection(slug, branch string, pageBuild *StatusBuild) (*StatusBuild, bool) {
	if pageBuild == nil {
		return nil, false
	}
	raw, ok := statusReadForgeEntryAny("full", "bitbucket.org", slug, "build:"+branch)
	if !ok || string(raw) != statusBuildFingerprint(pageBuild) {
		return nil, false
	}
	saved, ok := statusReadForgeEntryAny("build", "bitbucket.org", slug, branch)
	if !ok {
		return nil, false
	}
	savedBuild, err := statusParseBBBuild(saved, slug, branch)
	if err != nil {
		return nil, false
	}
	return savedBuild, true
}
