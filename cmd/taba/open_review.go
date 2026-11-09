package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"taba.sh/taba/internal/wtc"
)

var openReviewSHA = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

type openReviewRange struct {
	repo, dir, base, head, source string
}

func openReviewOutput(dir, program string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Dir = dir
	// A missing partial-clone object must not cause an implicit fetch.
	cmd.Env = append(os.Environ(), "GIT_NO_LAZY_FETCH=1", "GIT_NO_REPLACE_OBJECTS=1")
	if program != "bb" {
		return cmd.Output()
	}
	// Like the existing forge reader, use a regular file for CLIs that can
	// exit before a large JSON write to a pipe has drained.
	file, err := os.CreateTemp("", "wtc-open-review-*.json")
	if err != nil {
		return nil, err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	cmd.Stdout = file
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(file)
}

func openReviewGit(dir string, args ...string) (string, error) {
	out, err := openReviewOutput(dir, "git", args...)
	return strings.TrimSpace(string(out)), err
}

func openReviewPinned(repo, dir, base, head, source string) (openReviewRange, error) {
	for _, sha := range []string{base, head} {
		if !openReviewSHA.MatchString(sha) {
			return openReviewRange{}, errors.New("range requires full pinned commit IDs")
		}
		kind, err := openReviewGit(dir, "cat-file", "-t", sha)
		if err != nil || kind != "commit" {
			return openReviewRange{}, errors.New("pinned commit objects unavailable locally; fetch the intended range explicitly before opening review")
		}
	}
	bases, err := openReviewGit(dir, "merge-base", "--all", base, head)
	if err != nil || !openReviewSHA.MatchString(bases) {
		return openReviewRange{}, errors.New("review range has no unique local merge base")
	}
	return openReviewRange{repo: repo, dir: dir, base: bases, head: head, source: source}, nil
}

// Resolve only checked-out siblings. Manifest repo_dir is never a source path.
func openReviewRepo(c *wtc.Context, name string) (dir, slug, forge string, err error) {
	if name != "" && name == c.Config.Harness.Name {
		name = "harness"
	}
	dir, slug, forge, err = c.ReviewRepo(name)
	if err != nil {
		return
	}
	real, e := filepath.EvalSymlinks(dir)
	root, re := filepath.EvalSymlinks(c.Collection)
	if e != nil || re != nil || filepath.Dir(real) != root {
		return "", "", "", errors.New("review repository must be a checked-out collection sibling")
	}
	return
}

func openReviewBundleRange(c *wtc.Context) (openReviewRange, bool, error) {
	type candidate struct {
		m         wtc.ReviewManifest
		modified  time.Time
		dir, name string
	}
	latest := map[string]candidate{}
	for _, root := range wtc.ReviewBundleRoots(c.Collection) {
		if info, err := os.Lstat(root); err == nil && !info.IsDir() {
			return openReviewRange{}, false, errors.New("review bundle root must be a local directory")
		}
		entries, err := os.ReadDir(root)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return openReviewRange{}, false, err
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			path := filepath.Join(root, entry.Name(), "manifest.json")
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			data, err := os.ReadFile(path)
			var m wtc.ReviewManifest
			if err != nil || json.Unmarshal(data, &m) != nil || m.Collection != filepath.Base(c.Collection) {
				continue
			}
			dir, slug, forge, err := openReviewRepo(c, m.Repo)
			if err != nil || !strings.EqualFold(m.Slug, slug) || m.Forge != forge {
				continue
			}
			branch, err := openReviewGit(dir, "symbolic-ref", "--quiet", "--short", "HEAD")
			if err != nil || branch == "" || branch != m.HeadBranch {
				continue
			}
			prior, ok := latest[dir]
			if !ok || info.ModTime().After(prior.modified) || info.ModTime().Equal(prior.modified) && entry.Name() > prior.name {
				latest[dir] = candidate{m, info.ModTime(), dir, entry.Name()}
			}
		}
	}
	if len(latest) > 1 {
		return openReviewRange{}, true, errors.New("review bundles match multiple siblings; use a collection scoped to one reviewed repository")
	}
	for _, cand := range latest {
		r, err := openReviewPinned(cand.m.Repo, cand.dir, cand.m.BaseSHA, cand.m.HeadSHA, "bundle "+cand.name)
		return r, true, err
	}
	return openReviewRange{}, false, nil
}

type openReviewPR struct {
	Number   int    `json:"number"`
	State    string `json:"state"`
	Branch   string `json:"headRefName"`
	Head     string `json:"headRefOid"`
	Base     string `json:"baseRefOid"`
	HeadRepo struct {
		Name string `json:"nameWithOwner"`
	} `json:"headRepository"`
	ID          int                  `json:"id"`
	Source      openReviewBBEndpoint `json:"source"`
	Destination openReviewBBEndpoint `json:"destination"`
}
type openReviewBBEndpoint struct {
	Branch struct {
		Name string `json:"name"`
	} `json:"branch"`
	Commit struct {
		Hash string `json:"hash"`
	} `json:"commit"`
	Repository struct {
		Name string `json:"full_name"`
	} `json:"repository"`
}

func openReviewPRRange(c *wtc.Context) (openReviewRange, error) {
	rows, err := c.ListPRs()
	if err != nil {
		return openReviewRange{}, err
	}
	entries, err := os.ReadDir(c.Collection)
	if err != nil {
		return openReviewRange{}, err
	}
	var matches []openReviewRange
	var failures []error
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		repo := entry.Name()
		dir, slug, forge, err := openReviewRepo(c, repo)
		if err != nil {
			continue
		}
		branch, err := openReviewGit(dir, "symbolic-ref", "--quiet", "--short", "HEAD")
		if err != nil || branch == "" {
			continue
		}
		var numbers []string
		for _, row := range rows {
			rowRepo := row.Repo
			if rowRepo == c.Config.Harness.Name {
				rowRepo = "harness"
			}
			if rowRepo == repo && row.Branch == branch && row.MergedOn == "" {
				if wtc.ValidatePRIdentity(repo, row.Number) != nil {
					return openReviewRange{}, errors.New("invalid enlisted PR identity")
				}
				numbers = append(numbers, row.Number)
			}
		}
		if len(numbers) > 1 {
			return openReviewRange{}, errors.New("multiple PRs enlisted for the current branch")
		}
		fields := "number,state,headRefName,headRefOid,baseRefOid,headRepository"
		program, args := "gh", []string{"pr", "list", "--repo", slug, "--head", branch, "--state", "open", "--limit", "100", "--json", fields}
		if forge == "bitbucket" {
			program, args = "bb", []string{"pr", "list", "--state", "OPEN", "--limit", "100", "--json"}
		}
		if len(numbers) == 1 {
			args = []string{"pr", "view", numbers[0], "--repo", slug, "--json", fields}
			if forge == "bitbucket" {
				args = []string{"pr", "view", numbers[0], "--json"}
			}
		}
		if forge == "bitbucket" {
			owner, name, _ := strings.Cut(slug, "/")
			args = append(args, "--workspace", owner, "--repo", name)
		}
		data, err := openReviewOutput(dir, program, args...)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: cannot read current PR metadata", repo))
			continue
		}
		var prs []openReviewPR
		if len(numbers) == 1 {
			var pr openReviewPR
			err = json.Unmarshal(data, &pr)
			prs = append(prs, pr)
		} else if forge == "github" {
			err = json.Unmarshal(data, &prs)
		} else {
			var wrapped struct {
				Values       []openReviewPR `json:"values"`
				PullRequests []openReviewPR `json:"pullRequests"`
			}
			err = json.Unmarshal(data, &prs)
			if err != nil {
				err = json.Unmarshal(data, &wrapped)
				prs = wrapped.Values
				if prs == nil {
					prs = wrapped.PullRequests
				}
			}
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: malformed PR metadata", repo))
			continue
		}
		if len(numbers) == 0 && len(prs) >= 100 {
			return openReviewRange{}, errors.New("PR discovery reached its limit; enlist the intended current PR explicitly")
		}
		for _, pr := range prs {
			if forge == "bitbucket" {
				pr.Number, pr.Branch, pr.Head, pr.Base, pr.HeadRepo.Name = pr.ID, pr.Source.Branch.Name, pr.Source.Commit.Hash, pr.Destination.Commit.Hash, pr.Source.Repository.Name
				if !strings.EqualFold(pr.Destination.Repository.Name, slug) {
					continue
				}
			}
			if pr.State != "OPEN" || pr.Branch != branch || !strings.EqualFold(pr.HeadRepo.Name, slug) {
				continue
			}
			if pr.Number <= 0 || len(numbers) == 1 && fmt.Sprint(pr.Number) != numbers[0] {
				return openReviewRange{}, errors.New("PR identity does not match enlistment")
			}
			r, err := openReviewPinned(repo, dir, pr.Base, pr.Head, fmt.Sprintf("PR #%d", pr.Number))
			if err != nil {
				return openReviewRange{}, fmt.Errorf("%s: %w", repo, err)
			}
			matches = append(matches, r)
		}
	}
	if len(matches) > 1 {
		return openReviewRange{}, errors.New("multiple current PRs match this collection; build a review bundle for the intended range")
	}
	if len(failures) > 0 {
		return openReviewRange{}, errors.Join(failures...)
	}
	if len(matches) == 0 {
		return openReviewRange{}, errors.New("no applicable review bundle or current PR; build a review bundle on the checked-out branch")
	}
	return matches[0], nil
}

func openReviewCommand(c *wtc.Context) (command, detail string, err error) {
	r, found, err := openReviewBundleRange(c)
	if err != nil {
		return "", "", err
	}
	if !found {
		r, err = openReviewPRRange(c)
	}
	if err != nil {
		return "", "", err
	}
	return openReviewViewer(c, r)
}

func openReviewViewer(c *wtc.Context, r openReviewRange) (command, detail string, err error) {
	// Use a relative sibling path so pane commands can be rerun in a collection.
	rel, err := filepath.Rel(c.Collection, r.dir)
	if err != nil {
		return "", "", err
	}
	viewer := "hunk"
	if _, e := exec.LookPath("hunk"); e == nil {
		command = "(cd " + shellQuote(rel) + " && GIT_NO_LAZY_FETCH=1 GIT_NO_REPLACE_OBJECTS=1 hunk diff " + r.base + " " + r.head + ")"
	} else {
		if _, e := exec.LookPath("less"); e != nil {
			return "", "", errors.New("review needs hunk or less installed")
		}
		viewer = "git diff / less (hunk unavailable)"
		command = "(cd " + shellQuote(rel) + " && GIT_NO_LAZY_FETCH=1 GIT_NO_REPLACE_OBJECTS=1 git --no-pager diff --no-ext-diff --no-textconv --color=never " + r.base + " " + r.head + " -- | LESS= LESSOPEN= LESSCLOSE= LESSSECURE=1 less)"
	}
	detail = fmt.Sprintf("review %s: %s %s..%s via %s", r.source, r.repo, r.base, r.head, viewer)
	return
}
