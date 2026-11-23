package wtc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The built-in build providers answer the same question as a harness
// status.build.sh: what is the newest build of this branch, and which commit
// did it build. They are the default; a harness hook replaces them entirely.

const statusGHBuildQuery = `query($owner:String!,$name:String!,$ref:String!){repository(owner:$owner,name:$name){ref(qualifiedName:$ref){target{... on Commit{oid statusCheckRollup{state contexts(first:50){nodes{__typename ... on CheckRun{checkSuite{workflowRun{runNumber url}}} ... on StatusContext{targetUrl}}}}}}}}}`

func statusGHBuild(slug, branch string) (*StatusBuild, error) {
	return statusGHBuildContext(context.Background(), slug, branch)
}

func statusGHBuildContext(ctx context.Context, slug, branch string) (*StatusBuild, error) {
	parts := strings.SplitN(slug, "/", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid GitHub repository slug")
	}
	raw, err := statusSingleFlightContext(ctx, "build", "github.com", slug, branch, func() ([]byte, error) {
		return statusJSONContext(ctx, "gh", "api", "graphql", "-f", "query="+statusGHBuildQuery,
			"-f", "owner="+parts[0], "-f", "name="+parts[1], "-f", "ref=refs/heads/"+branch)
	})
	if err != nil {
		return nil, err
	}
	return statusParseGHBuild(raw, slug, branch)
}

func statusParseGHBuild(raw []byte, slug, branch string) (*StatusBuild, error) {
	var response struct {
		Data struct {
			Repository struct {
				Ref *struct {
					Target struct {
						OID    string `json:"oid"`
						Rollup *struct {
							State    string `json:"state"`
							Contexts struct {
								Nodes []struct {
									Type       string `json:"__typename"`
									CheckSuite struct {
										WorkflowRun *struct {
											RunNumber int    `json:"runNumber"`
											URL       string `json:"url"`
										} `json:"workflowRun"`
									} `json:"checkSuite"`
									TargetURL string `json:"targetUrl"`
								} `json:"nodes"`
							} `json:"contexts"`
						} `json:"statusCheckRollup"`
					} `json:"target"`
				} `json:"ref"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, fmt.Errorf("GitHub build JSON: %w", err)
	}
	ref := response.Data.Repository.Ref
	if ref == nil || ref.Target.OID == "" || ref.Target.Rollup == nil {
		// No branch, or a commit nothing reported on: no build to show.
		return nil, nil
	}
	build := &StatusBuild{Branch: branch, SHA: statusString(ref.Target.OID)}
	switch state := strings.ToUpper(ref.Target.Rollup.State); state {
	case "SUCCESS", "FAILURE", "PENDING", "ERROR", "EXPECTED":
		build.Checks = statusString(state)
	default:
		build.Checks = statusString("NONE")
	}
	link := "https://github.com/" + slug + "/commit/" + ref.Target.OID + "/checks"
	for _, node := range ref.Target.Rollup.Contexts.Nodes {
		if run := node.CheckSuite.WorkflowRun; node.Type == "CheckRun" && run != nil && run.RunNumber != 0 {
			build.Build = statusString(strconv.Itoa(run.RunNumber))
			if run.URL != "" {
				link = run.URL
			}
			break
		}
	}
	build.URL = statusString(link)
	return build, nil
}

// statusBBAPI is the Bitbucket REST root; tests point it at a local server.
var statusBBAPI = "https://api.bitbucket.org/2.0"

// bitbucketRESTCredentials returns the credential for one repository's REST
// reads in this operation. With a host read credential (#471) it is that
// repository's token as a bearer token: user is empty and pass holds it, for
// that request only. Otherwise it is bitbucketCredentials. err explains a
// missing credential without naming any value.
func bitbucketRESTCredentials(ctx context.Context, slug string) (user, pass string, err error) {
	workspace, repo, err := splitBitbucketSlug(slug)
	if err != nil {
		return "", "", err
	}
	token, host, err := bitbucketReadFor(ctx).restBearer(ctx, workspace, repo)
	if err != nil {
		return "", "", err
	}
	if host {
		return "", token, nil
	}
	user, pass = bitbucketCredentials()
	if user == "" || pass == "" {
		return "", "", fmt.Errorf("no Bitbucket API credentials (set BB_USERNAME and BB_API_TOKEN, or opt this host into %s=git-credential)", bitbucketReadCredentialVariable)
	}
	return user, pass, nil
}

// bitbucketCredentials reads the API user and token from the environment
// first, then from the bb CLI's config file (read as a file; bb itself is not
// run or required).
func bitbucketCredentials() (string, string) {
	for _, pair := range [][2]string{{"BB_API_USER", "BB_API_PASS"}, {"BB_USERNAME", "BB_API_TOKEN"}, {"ATLASSIAN_ACCOUNT_EMAIL", "ATLASSIAN_API_TOKEN"}} {
		if user, pass := os.Getenv(pair[0]), os.Getenv(pair[1]); user != "" && pass != "" {
			return user, pass
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", ""
	}
	raw, err := os.ReadFile(filepath.Join(home, ".config", "bb", "config.json"))
	if err != nil {
		return "", ""
	}
	var config struct {
		Username string `json:"username"`
		APIToken string `json:"apiToken"`
	}
	if json.Unmarshal(raw, &config) != nil {
		return "", ""
	}
	return config.Username, config.APIToken
}

func statusBBBuild(slug, branch string) (*StatusBuild, error) {
	return statusBBBuildContext(context.Background(), slug, branch)
}

func statusBBBuildContext(ctx context.Context, slug, branch string) (*StatusBuild, error) {
	ctx = statusAuthOperation(ctx)
	parts := strings.SplitN(slug, "/", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid Bitbucket repository slug")
	}
	user, pass, err := bitbucketRESTCredentials(ctx, slug)
	if err != nil {
		return nil, err
	}
	var started time.Time
	raw, live, err := statusFetchSharedContext(ctx, "pipelines", "bitbucket.org", slug, "recent", func() ([]byte, error) {
		started = time.Now()
		return statusBBPipelinesContext(ctx, parts[0], parts[1], "", user, pass)
	})
	if err != nil {
		return nil, err
	}
	if live {
		statusSaveLivePipelinePageContext(ctx, slug, raw, started)
	}
	if pageSince, ok := statusForgeEntrySinceContext(ctx, "pipelines", "bitbucket.org", slug, "recent"); ok {
		if buildSince, ok := statusForgeEntrySinceContext(ctx, "build", "bitbucket.org", slug, branch); ok && buildSince.After(pageSince) {
			if saved, ok := statusReadForgeEntryAnyContext(ctx, "build", "bitbucket.org", slug, branch); ok {
				raw = saved
			}
		}
	}
	build, err := statusParseBBBuild(raw, slug, branch)
	if err != nil {
		return nil, err
	}
	if build != nil {
		if audited, found := statusPreferAuditedBuildContext(ctx, slug, branch, build); found {
			return audited, nil
		}
		return statusConfirmFullBuildContext(ctx, slug, branch, build, "page", user, pass), nil
	}
	if saved, ok := statusReadForgeEntryAnyContext(ctx, "build", "bitbucket.org", slug, branch); ok {
		savedBuild, parseErr := statusParseBBBuild(saved, slug, branch)
		if parseErr != nil {
			return nil, parseErr
		}
		return statusConfirmFullBuildContext(ctx, slug, branch, savedBuild, "saved", user, pass), nil
	}
	raw, err = statusSingleFlightContext(ctx, "build", "bitbucket.org", slug, branch, func() ([]byte, error) {
		return statusBBPipelinesContext(ctx, parts[0], parts[1], branch, user, pass)
	})
	if err != nil {
		return nil, err
	}
	build, err = statusParseBBBuild(raw, slug, branch)
	if err != nil {
		return nil, err
	}
	statusMarkFullRefreshContext(ctx, "bitbucket.org", slug, "build:"+branch)
	return build, nil
}

func statusBBPipelines(workspace, repo, branch, user, pass string) ([]byte, error) {
	return statusBBPipelinesContext(context.Background(), workspace, repo, branch, user, pass)
}

func statusBBPipelinesContext(ctx context.Context, workspace, repo, branch, user, pass string) ([]byte, error) {
	query := url.Values{"sort": {"-created_on"}, "pagelen": {"30"}}
	if branch != "" {
		query.Set("target.branch", branch)
	}
	endpoint := statusBBAPI + "/repositories/" + url.PathEscape(workspace) + "/" + url.PathEscape(repo) + "/pipelines/?" + query.Encode()
	// The outer admission pays the first hop; redirects spend the same bucket.
	return statusBBGETWithOptionsContext(ctx, StatusRefreshBackground, endpoint, user, pass, true)
}

func statusSaveLivePipelinePage(slug string, raw []byte, started time.Time) {
	statusSaveLivePipelinePageContext(context.Background(), slug, raw, started)
}

func statusSaveLivePipelinePageContext(ctx context.Context, slug string, raw []byte, started time.Time) {
	ctx = statusAuthOperation(ctx)
	for _, branch := range statusPipelinePageBranches(raw) {
		unlock, lockErr := statusLockCacheContext(ctx, statusForgeEntryPathContext(ctx, "build", "bitbucket.org", slug, branch))
		if lockErr != nil {
			return
		}
		build, err := statusParseBBBuild(raw, slug, branch)
		if err == nil && build != nil && !statusPageAuditAppliesContext(ctx, slug, branch, build) {
			statusResourceWriteSince(ctx, "build", "bitbucket.org", slug, branch, raw, started)
		}
		unlock()
	}
}

func statusPipelinePageBranches(raw []byte) []string {
	var page struct {
		Values []struct {
			Target struct {
				RefName  string `json:"ref_name"`
				Source   string `json:"source"`
				Selector struct {
					Type string `json:"type"`
				} `json:"selector"`
			} `json:"target"`
		} `json:"values"`
	}
	if json.Unmarshal(raw, &page) != nil {
		return nil
	}
	seen := map[string]bool{}
	var branches []string
	for _, run := range page.Values {
		name := run.Target.RefName
		if name == "" && statusPRPipelineSelector(run.Target.Selector.Type) {
			name = run.Target.Source
		}
		if name == "" || seen[name] {
			continue
		}
		if !statusHeadPipelineSelector(run.Target.Selector.Type) {
			continue
		}
		seen[name] = true
		branches = append(branches, name)
	}
	return branches
}

func statusPageAuditApplies(slug, branch string, pageBuild *StatusBuild) bool {
	return statusPageAuditAppliesContext(context.Background(), slug, branch, pageBuild)
}

func statusPageAuditAppliesContext(ctx context.Context, slug, branch string, pageBuild *StatusBuild) bool {
	ctx = statusAuthOperation(ctx)
	if pageBuild == nil {
		return false
	}
	raw, ok := statusReadForgeEntryAnyContext(ctx, "full", "bitbucket.org", slug, "build:"+branch)
	return ok && string(raw) == statusBuildFingerprint(pageBuild)
}

func statusParseBBBuild(raw []byte, slug, branch string) (*StatusBuild, error) {
	var page struct {
		Values []struct {
			BuildNumber int `json:"build_number"`
			State       struct {
				Name   string `json:"name"`
				Result struct {
					Name string `json:"name"`
				} `json:"result"`
			} `json:"state"`
			Target struct {
				RefType  string `json:"ref_type"`
				RefName  string `json:"ref_name"`
				Source   string `json:"source"`
				Selector struct {
					Type string `json:"type"`
				} `json:"selector"`
				Commit struct {
					Hash string `json:"hash"`
				} `json:"commit"`
			} `json:"target"`
		} `json:"values"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		return nil, fmt.Errorf("Bitbucket pipelines JSON: %w", err)
	}
	for _, run := range page.Values {
		// PR pipelines build the source head too; custom pipelines remain separate.
		name := run.Target.RefName
		if name == "" && statusPRPipelineSelector(run.Target.Selector.Type) {
			name = run.Target.Source
		}
		if name != branch || !statusHeadPipelineSelector(run.Target.Selector.Type) {
			continue
		}
		build := &StatusBuild{Branch: branch, Build: statusString(strconv.Itoa(run.BuildNumber)),
			SHA: statusString(run.Target.Commit.Hash),
			URL: statusString(fmt.Sprintf("https://bitbucket.org/%s/pipelines/results/%d", slug, run.BuildNumber))}
		checks := "PENDING"
		if strings.EqualFold(run.State.Name, "COMPLETED") {
			switch strings.ToUpper(run.State.Result.Name) {
			case "SUCCESSFUL":
				checks = "SUCCESS"
			case "FAILED", "ERROR", "STOPPED":
				checks = "FAILURE"
			}
		}
		build.Checks = statusString(checks)
		return build, nil
	}
	return nil, nil
}

func statusForgeBuild(forge, slug, branch string) (*StatusBuild, error) {
	return statusForgeBuildContext(context.Background(), forge, slug, branch)
}

func statusForgeBuildContext(ctx context.Context, forge, slug, branch string) (*StatusBuild, error) {
	switch forge {
	case "github.com":
		return statusGHBuildContext(ctx, slug, branch)
	case "bitbucket.org":
		return statusBBBuildContext(ctx, slug, branch)
	}
	return nil, nil
}

func statusHeadPipelineSelector(selector string) bool {
	switch selector {
	case "", "branches", "default", "pullrequests", "pull-requests":
		return true
	}
	return false
}

func statusPRPipelineSelector(selector string) bool {
	return selector == "pullrequests" || selector == "pull-requests"
}
