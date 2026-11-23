package wtc

import (
	"context"
	"time"
)

// BitbucketReadDiagnosis is doctor's row for one Bitbucket repository's read
// path (#471): which client reads it, with which credential, and — only when
// a probe was asked for explicitly — whether one bounded read succeeds.
type BitbucketReadDiagnosis struct {
	Repo       string `json:"repo"`
	Slug       string `json:"slug"`
	Client     string `json:"client"`
	Credential string `json:"credential"`
	Probed     bool   `json:"probed"`
	OK         bool   `json:"ok"`
	Detail     string `json:"detail"`
}

// bitbucketProbeTimeout bounds one repository's whole probe.
var bitbucketProbeTimeout = 30 * time.Second

// BitbucketReadDiagnose describes repo's read path. Without probe it reads
// local configuration only: no credential helper, client or network. With
// probe it runs, through the selected client and credential, one repository
// read and one one-item pull request list (which needs the pull request read
// scope), and reports the classified result; values, responses and
// credentials are never echoed.
func BitbucketReadDiagnose(root, repo, slug string, probe bool) BitbucketReadDiagnosis {
	d := BitbucketReadDiagnosis{Repo: repo, Slug: slug}
	selection := selectedBitbucketRead(root)
	switch selection.Client {
	case bitbucketClientTWG:
		d.Client = "twg (" + selection.ClientSource + ")"
	case bitbucketClientBB:
		d.Client = "bb (" + selection.ClientSource + "; deprecated)"
	default:
		d.Client = "unknown"
	}
	switch selection.Credential {
	case bitbucketCredentialGit:
		d.Credential = "host git credential helper, per repository (" + selection.CredentialSource + ")"
	case bitbucketCredentialPersonal:
		d.Credential = "the client's own login"
	default:
		d.Credential = "unknown"
	}
	if selection.err != nil {
		d.Detail = selection.err.Error()
		return d
	}
	if !probe {
		d.OK = true
		d.Detail = "not probed; run doctor --bitbucket-read-probe to try one read"
		return d
	}
	d.Probed = true
	ctx, cancel := context.WithTimeout(context.Background(), bitbucketProbeTimeout)
	defer cancel()
	ctx = bitbucketReadOperation(ctx, root)
	if _, err := bitbucketReadRepo(ctx, slug); err != nil {
		d.Detail = "repository read failed: " + err.Error()
		return d
	}
	if _, err := bitbucketReadPRsByState(ctx, slug, "", "OPEN", 1); err != nil {
		d.Detail = "repository readable, pull request read failed (pull request read scope?): " + err.Error()
		return d
	}
	d.OK = true
	d.Detail = "repository and pull requests readable"
	return d
}
